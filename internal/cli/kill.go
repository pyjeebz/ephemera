package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"ephemera.run/ephemera/internal/daemon"
)

// runKill asks the relay to force-expire a share. The daemon's own
// record is swept locally too.
func runKill(args []string) error {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	relay := fs.String("relay", "http://localhost:7100", "relay base URL")
	token := fs.String("token", os.Getenv("EPHEMERA_ADMIN_TOKEN"), "admin token (or EPHEMERA_ADMIN_TOKEN)")
	insecure := fs.Bool("insecure", false, "allow plain http to a relay off this machine (your admin token travels unencrypted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if u, err := url.Parse(*relay); err != nil {
		return err
	} else if u.Scheme != "https" && !*insecure && !daemon.IsLocalHost(u.Hostname()) {
		return fmt.Errorf("%w (%s): use https://, or pass -insecure", daemon.ErrPlaintext, u.Redacted())
	}
	if *token == "" {
		return errors.New("admin token required: -token or EPHEMERA_ADMIN_TOKEN")
	}
	if fs.NArg() != 1 {
		return errors.New("usage: ephemera kill [-relay url] <share-id>")
	}
	id := fs.Arg(0)

	req, err := http.NewRequest("POST", strings.TrimSuffix(*relay, "/")+"/admin/kill", strings.NewReader(`{"id":"`+id+`"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+*token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("share not found on relay (already gone?)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("relay refused: %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	fmt.Fprintf(os.Stderr, "killed %s on the relay\n", id)

	// sweep the local record so resume does not resurrect it
	if sd := openState(); sd != nil {
		sd.Remove(id)
		fmt.Fprintln(os.Stderr, "local record removed")
	}
	return nil
}

func init() {
	register("kill", runKill)
}
