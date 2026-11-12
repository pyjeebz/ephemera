package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/term"

	"github.com/pyjeebz/ephemera/internal/api"
	"github.com/pyjeebz/ephemera/internal/client"
	"github.com/pyjeebz/ephemera/internal/config"
)

func cmdLogin(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	saved, _ := config.Load()
	defServer := saved.Server
	if v := os.Getenv("EPHEMERA_SERVER"); v != "" {
		defServer = v
	}
	if defServer == "" {
		defServer = config.DefaultServer
	}
	server := fs.String("server", defServer, "central server URL")
	withToken := fs.Bool("with-token", false, "read an API token from stdin instead of signing in through the browser")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: ephemera login [--server URL] [--with-token]\n\n"+
			"In a terminal, signs this machine in through the browser: it shows a code,\n"+
			"opens the approval page, and saves the token once you approve. With\n"+
			"--with-token, piped input, or a server without browser sign-in, it reads\n"+
			"an API token instead (made with ephemera-server token create <name>, or\n"+
			"in the dashboard).\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var token string
	if !*withToken && isTerminal(stdin) {
		res, err := browserLogin(ctx, *server, stderr)
		switch {
		case errors.Is(err, client.ErrNoBrowserLogin):
			fmt.Fprintln(stderr, "This server has no browser sign-in. Paste an API token instead.")
		case err != nil:
			fmt.Fprintln(stderr, "ephemera: login:", err)
			return exitFailure
		default:
			token = res.Token
		}
	}
	if token == "" {
		t, err := readToken(stdin, stderr)
		if err != nil {
			fmt.Fprintln(stderr, "ephemera:", err)
			return exitFailure
		}
		token = t
	}
	c := &client.Client{Server: strings.TrimRight(*server, "/"), Token: token}
	who, err := c.WhoAmI(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "ephemera: login to %s failed: %v\n", c.Server, err)
		return exitFailure
	}
	if err := config.Save(config.Config{Server: c.Server, Token: token}); err != nil {
		fmt.Fprintln(stderr, "ephemera: saving login:", err)
		return exitFailure
	}
	p, _ := config.Path()
	fmt.Fprintf(stdout, "Logged in to %s as %s (saved in %s)\n", c.Server, who.Name, p)
	return 0
}

// browserLogin shows the code and the approval page, opens the page if it
// can, and waits for the approval.
func browserLogin(ctx context.Context, server string, stderr io.Writer) (api.DeviceToken, error) {
	name, _ := os.Hostname()
	return client.DeviceLogin(ctx, server, name, func(dc api.DeviceCode) {
		fmt.Fprintf(stderr, "To sign in, open this page and check it shows %s:\n\n  %s\n\n", dc.UserCode, dc.VerificationURI)
		if os.Getenv("EPHEMERA_NO_BROWSER") == "" {
			openBrowser(dc.VerificationURI)
		}
		fmt.Fprintln(stderr, "Waiting for you to approve it… (Ctrl-C to stop)")
	})
}

// openBrowser opens a page in the default browser, best effort: if it
// can't, the page is printed anyway.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		name := "xdg-open"
		if _, err := exec.LookPath("wslview"); err == nil {
			name = "wslview" // WSL: open it in the Windows browser
		}
		cmd = exec.Command(name, url)
	}
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// readToken prompts for the token without echoing it when stdin is a
// terminal, and otherwise reads one line, so scripts can pipe it in.
func readToken(stdin io.Reader, stderr io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stderr, "API token: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nonEmpty(string(b))
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nonEmpty(line)
}

func nonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("no token given")
	}
	return nil
}

func cmdLogout(stdout, stderr io.Writer) int {
	if err := config.Remove(); err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
	}
	fmt.Fprintln(stdout, "Logged out")
	return 0
}
