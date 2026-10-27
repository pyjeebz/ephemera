package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"ephemera.run/ephemera/internal/daemon"
)

// adminClient talks to a relay's /admin API with the admin token.
type adminClient struct {
	base  string
	token string
}

// adminFlags registers the flags every admin command shares and
// returns a constructor to call after parsing.
func adminFlags(fs *flag.FlagSet) func() (adminClient, error) {
	relay := fs.String("relay", "http://localhost:7100", "relay base URL")
	token := fs.String("token", os.Getenv("EPHEMERA_ADMIN_TOKEN"), "admin token (or EPHEMERA_ADMIN_TOKEN)")
	insecure := fs.Bool("insecure", false, "allow plain http to a relay off this machine (your admin token travels unencrypted)")
	return func() (adminClient, error) {
		if *token == "" {
			return adminClient{}, errors.New("admin token required: -token or EPHEMERA_ADMIN_TOKEN")
		}
		u, err := url.Parse(*relay)
		if err != nil {
			return adminClient{}, err
		}
		if u.Scheme != "https" && !*insecure && !daemon.IsLocalHost(u.Hostname()) {
			return adminClient{}, fmt.Errorf("%w (%s): use https://, or pass -insecure", daemon.ErrPlaintext, u.Redacted())
		}
		return adminClient{base: strings.TrimSuffix(*relay, "/"), token: *token}, nil
	}
}

// do sends one admin request and decodes a JSON reply into out. It
// returns the HTTP status; any non-2xx comes back as an error carrying
// the relay's message, except the statuses listed in ok.
func (c adminClient) do(method, path string, body, out any, ok ...int) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	accepted := resp.StatusCode/100 == 2
	for _, s := range ok {
		accepted = accepted || resp.StatusCode == s
	}
	if !accepted {
		var e struct{ Error string }
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return resp.StatusCode, fmt.Errorf("relay refused: %s: %s", resp.Status, e.Error)
		}
		return resp.StatusCode, fmt.Errorf("relay refused: %s: %s", resp.Status, bytes.TrimSpace(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("relay reply: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// runKill asks the relay to force-expire a share, and with -suspend
// to suspend its sender too. The daemon's own record is swept
// locally.
func runKill(args []string) error {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	client := adminFlags(fs)
	suspend := fs.Bool("suspend", false, "also suspend the sender's account and kill its other links")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usage("usage: ephemera kill [-relay url] [-suspend] <share-id>")
	}
	id := fs.Arg(0)

	var reply struct {
		Killed     bool
		Suspended  string
		AlsoKilled []string `json:"also_killed"`
	}
	status, err := c.do("POST", "/admin/kill", map[string]any{"id": id, "suspend": *suspend}, &reply, http.StatusNotFound)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return errors.New("share not found on relay (already gone?)")
	}
	if reply.Killed {
		fmt.Fprintf(os.Stderr, "killed %s on the relay\n", id)
	} else {
		fmt.Fprintf(os.Stderr, "%s was already gone from the relay\n", id)
	}
	if reply.Suspended != "" {
		fmt.Fprintf(os.Stderr, "suspended account %s", reply.Suspended)
		if n := len(reply.AlsoKilled); n > 0 {
			fmt.Fprintf(os.Stderr, " and killed %d more of its links", n)
		}
		fmt.Fprintln(os.Stderr)
	}

	// sweep the local record so resume does not resurrect it
	if sd := openState(); sd != nil {
		sd.Remove(id)
	}
	return nil
}

// runReports lists the relay's link reports, newest first.
func runReports(args []string) error {
	fs := flag.NewFlagSet("reports", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	client := adminFlags(fs)
	n := fs.Int("n", 50, "show at most this many (0 = all)")
	asJSON := fs.Bool("json", false, "print the raw JSON")
	if err := parse(fs, args); err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	var reports []struct {
		At      time.Time `json:"at"`
		ShareID string    `json:"share_id"`
		Reason  string    `json:"reason"`
		Note    string    `json:"note"`
		Name    string    `json:"name"`
		Size    int64     `json:"size"`
		Account string    `json:"account"`
	}
	var raw json.RawMessage
	if _, err := c.do("GET", fmt.Sprintf("/admin/reports?limit=%d", *n), nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		_, err := os.Stdout.Write(append(raw, '\n'))
		return err
	}
	if err := json.Unmarshal(raw, &reports); err != nil {
		return err
	}
	if len(reports) == 0 {
		fmt.Fprintln(os.Stderr, "no reports")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tLINK\tREASON\tACCOUNT\tFILE\tNOTE")
	for _, r := range reports {
		account := r.Account
		if account == "" {
			account = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s (%d B)\t%s\n",
			r.At.Local().Format("2006-01-02 15:04"), r.ShareID, r.Reason, account, r.Name, r.Size, oneLine(r.Note, 60))
	}
	return tw.Flush()
}

// runSuspend suspends a sender account and kills its live links.
func runSuspend(args []string) error {
	fs := flag.NewFlagSet("suspend", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	client := adminFlags(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usage("usage: ephemera suspend [-relay url] <account>")
	}
	var reply struct{ Killed []string }
	if _, err := c.do("POST", "/admin/suspend", map[string]string{"account": fs.Arg(0)}, &reply); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "suspended %s; killed %d live link(s)\n", fs.Arg(0), len(reply.Killed))
	return nil
}

// runUnsuspend lifts a suspension.
func runUnsuspend(args []string) error {
	fs := flag.NewFlagSet("unsuspend", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	client := adminFlags(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usage("usage: ephemera unsuspend [-relay url] <account>")
	}
	if _, err := c.do("POST", "/admin/unsuspend", map[string]string{"account": fs.Arg(0)}, nil); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "unsuspended %s\n", fs.Arg(0))
	return nil
}

// runSuspended lists suspended accounts, one per line.
func runSuspended(args []string) error {
	fs := flag.NewFlagSet("suspended", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	client := adminFlags(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	var list []string
	if _, err := c.do("GET", "/admin/suspended", nil, &list); err != nil {
		return err
	}
	for _, a := range list {
		fmt.Println(a)
	}
	return nil
}

// oneLine flattens s to a single line of at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func init() {
	register("kill", runKill)
	register("reports", runReports)
	register("suspend", runSuspend)
	register("unsuspend", runUnsuspend)
	register("suspended", runSuspended)
}
