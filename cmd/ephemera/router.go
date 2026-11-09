package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pyjeebz/ephemera/internal/config"
	"github.com/pyjeebz/ephemera/internal/router"
)

func routerClient() (*router.Client, error) {
	sock, err := config.RouterSocket()
	if err != nil {
		return nil, err
	}
	return &router.Client{Socket: sock}, nil
}

// cmdRouter runs the local router. ephemera dev starts it when needed; it
// exits a while after the last session ends.
func cmdRouter(ctx context.Context, stderr io.Writer) int {
	sock, err := config.RouterSocket()
	if err != nil {
		fmt.Fprintln(stderr, "ephemera router:", err)
		return exitFailure
	}
	os.MkdirAll(filepath.Dir(sock), 0o700)
	srv := &router.Server{Socket: sock, IdleExit: 30 * time.Second}
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintln(stderr, "ephemera router:", err)
		return exitFailure
	}
	return 0
}

// startRouter starts "ephemera router" in the background and waits for it.
func startRouter(rc *router.Client) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(rc.Socket), 0o700)
	cmd := exec.Command(self, "router")
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap it if it exits while we run
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if rc.Running() {
			return nil
		}
	}
	return errors.New("the router did not start")
}

func cmdRoutes(ctx context.Context, stdout, stderr io.Writer) int {
	rc, err := routerClient()
	if err == nil && !rc.Running() {
		fmt.Fprintln(stdout, "No dev sessions share ports right now.")
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
	}
	routes, err := rc.Routes(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ADDRESS\tSESSION\tLOCALHOST")
	for _, r := range routes {
		bare := ""
		if r.Focused {
			bare = fmt.Sprintf("localhost:%d", r.Port)
		}
		fmt.Fprintf(tw, "http://%s.localhost:%d\t%s\t%s\n", r.Name, r.Port, r.Name, bare)
	}
	tw.Flush()
	return 0
}

func cmdFocus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Usage: ephemera focus <session-name>")
		return 2
	}
	rc, err := routerClient()
	if err == nil {
		err = rc.Focus(ctx, args[0], 0)
	}
	if err != nil {
		fmt.Fprintln(stderr, "ephemera:", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "Plain localhost now shows %s.\n", router.Label(args[0]))
	return 0
}

// sessionName is the dev session's name: the git branch, else the folder.
func sessionName(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if b := strings.TrimSpace(string(out)); err == nil && b != "" && b != "HEAD" {
		if l := router.Label(b); l != "" {
			return l
		}
	}
	abs, _ := filepath.Abs(dir)
	if l := router.Label(filepath.Base(abs)); l != "" {
		return l
	}
	return "dev"
}
