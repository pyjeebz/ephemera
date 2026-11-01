package cli

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ephemera.run/ephemera/internal/daemon"
)

// linkRecord is what `ephemera revoke` needs to delete a link this
// machine shared. send's resume record goes when send exits; this one
// stays until the link expires, so a link can be deleted after its
// upload finished.
type linkRecord struct {
	ID       string `json:"id"`
	OwnerKey string `json:"owner_key"` // secret: deletes the link
	Link     string `json:"link"`
	Name     string `json:"name"`
	Expires  int64  `json:"expires"` // unix seconds
}

func linksDir() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "links"), nil
}

// saveLink records a link's owner key, best-effort: without it the
// link still works, it just can't be revoked from here. Expired
// records are swept on the way.
func saveLink(rec linkRecord) {
	dir, err := linksDir()
	if err != nil || rec.ID == "" || rec.OwnerKey == "" {
		return
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	sweepLinks(dir)
	b, _ := json.Marshal(rec)
	os.WriteFile(filepath.Join(dir, rec.ID+".json"), b, 0o600)
}

func sweepLinks(dir string) {
	entries, _ := os.ReadDir(dir)
	now := time.Now().Unix()
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		var rec linkRecord
		if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &rec) != nil || rec.Expires <= now {
			os.Remove(path)
		}
	}
}

// linkID takes a link, its /file URL or a bare id to the id.
func linkID(s string) string {
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), "/file")
	return s[strings.LastIndex(s, "/")+1:]
}

// runRevoke deletes a link this machine shared: the relay checks the
// owner key, takes the link down and deletes its copy.
func runRevoke(args []string) error {
	fs := flag.NewFlagSet("revoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, `print {"event":"revoked",...} on stdout`)
	insecure := fs.Bool("insecure", false, insecureUsage)
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usage("usage: ephemera revoke [-json] <link>")
	}
	id := linkID(fs.Arg(0))
	dir, err := linksDir()
	if err != nil {
		return err
	}
	var rec linkRecord
	b, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil || json.Unmarshal(b, &rec) != nil || rec.OwnerKey == "" {
		return fmt.Errorf("this machine has no key for %s: only the sender can delete a link (it may also have expired)", id)
	}
	u, err := url.Parse(rec.Link)
	if err != nil {
		return err
	}
	if u.Scheme != "https" && !*insecure && !daemon.IsLocalHost(u.Hostname()) {
		return fmt.Errorf("%w (%s): the owner key would travel unencrypted; pass -insecure to send it anyway", daemon.ErrPlaintext, u.Redacted())
	}

	body, _ := json.Marshal(map[string]string{"owner_key": rec.OwnerKey})
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Post(rec.Link+"/revoke", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var reply struct{ Error string }
	json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&reply)
	switch resp.StatusCode {
	case http.StatusOK:
		fmt.Fprintf(os.Stderr, "deleted %s: the relay took it down and dropped its copy\n", rec.Link)
	case http.StatusNotFound:
		fmt.Fprintf(os.Stderr, "%s was already gone from the relay\n", rec.Link)
	default:
		return errors.New("the relay didn't delete it: " + cmp.Or(reply.Error, resp.Status))
	}
	// forget it here too, so resume doesn't bring it back
	os.Remove(filepath.Join(dir, id+".json"))
	if sd := openState(); sd != nil {
		sd.Remove(id)
	}
	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"event": "revoked", "id": id, "link": rec.Link, "already_gone": resp.StatusCode == http.StatusNotFound})
	}
	return nil
}

func init() {
	register("revoke", runRevoke)
}
