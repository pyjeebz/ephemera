package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

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
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: ephemera login [--server URL]\n\n"+
			"Reads an API token, checks it with the server and saves it.\n"+
			"Create a token on the server with: ephemera-server token create <name>\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	token, err := readToken(stdin, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
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
