package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pyjeebz/ephemera/internal/agent"
	"github.com/pyjeebz/ephemera/internal/client"
)

// maxSCP caps a copy, because the transfer rides on the exec channel: the file's
// bytes are base64'd into a shell command, and a command line has a size limit.
// Enough for configs, scripts, and small archives; a streaming transfer is the
// follow-up for anything bigger.
const maxSCP = 1 << 20 // 1 MiB

func cmdSCP(argv []string) error {
	fs := flag.NewFlagSet("scp", flag.ExitOnError)
	addr := daemonAddr(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ephemera scp <src> <dst>\n\n"+
			"Copies a file in or out of a box. One side is box:path, the other local:\n"+
			"  eph scp ./app.py dev:/root/app.py    # into the box\n"+
			"  eph scp dev:/root/out.txt ./out.txt  # out of the box\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("need a source and a destination")
	}

	src, dst := fs.Arg(0), fs.Arg(1)
	srcBox, srcPath, srcRemote := splitTarget(src)
	dstBox, dstPath, dstRemote := splitTarget(dst)
	if srcRemote == dstRemote {
		return fmt.Errorf("exactly one of src and dst must be box:path")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl := client.New(*addr)

	if dstRemote {
		return scpUpload(ctx, cl, src, dstBox, dstPath)
	}
	return scpDownload(ctx, cl, srcBox, srcPath, dst)
}

// splitTarget reads a "box:path" argument. A local path (no colon, or a colon
// after a slash as in a relative time-looking name) has remote=false.
func splitTarget(arg string) (box, path string, remote bool) {
	i := strings.IndexByte(arg, ':')
	if i <= 0 {
		return "", arg, false
	}
	// A box name has no slash; if the part before the colon contains one, this is
	// a local path that merely has a colon in it.
	if strings.ContainsAny(arg[:i], "/.") {
		return "", arg, false
	}
	return arg[:i], arg[i+1:], true
}

func scpUpload(ctx context.Context, cl *client.Client, localPath, box, remotePath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	if len(data) > maxSCP {
		return fmt.Errorf("%s is %d bytes; scp is limited to %d for now", localPath, len(data), maxSCP)
	}
	id, err := resolveMachineID(ctx, cl, box)
	if err != nil {
		return err
	}

	enc := base64.StdEncoding.EncodeToString(data)
	cmd := fmt.Sprintf("mkdir -p \"$(dirname %s)\" && printf %%s %s | base64 -d > %s",
		shellQuote(remotePath), enc, shellQuote(remotePath))

	var stderr bytes.Buffer
	code, err := cl.Exec(ctx, id, agent.ExecRequest{Cmd: []string{"sh", "-c", cmd}}, nil, &stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("write failed: %s", strings.TrimSpace(stderr.String()))
	}
	fmt.Fprintf(os.Stderr, "ephemera: copied %d bytes to %s:%s\n", len(data), box, remotePath)
	return nil
}

func scpDownload(ctx context.Context, cl *client.Client, box, remotePath, localPath string) error {
	id, err := resolveMachineID(ctx, cl, box)
	if err != nil {
		return err
	}

	// base64 in the guest so arbitrary bytes survive the exec stream intact.
	var out, stderr bytes.Buffer
	code, err := cl.Exec(ctx, id, agent.ExecRequest{Cmd: []string{"base64", remotePath}}, &out, &stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("read failed: %s", strings.TrimSpace(stderr.String()))
	}
	data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(out.String()), ""))
	if err != nil {
		return fmt.Errorf("decode file: %w", err)
	}
	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "ephemera: copied %d bytes from %s:%s\n", len(data), box, remotePath)
	return nil
}

// resolveMachineID turns a box (a computer name or a machine id) into the id of
// its running machine.
func resolveMachineID(ctx context.Context, cl *client.Client, box string) (string, error) {
	if c, err := cl.GetComputer(ctx, box); err == nil {
		if !c.Running {
			return "", fmt.Errorf("box %q is stopped — 'ephemera start %s' first", box, box)
		}
		return c.MachineID, nil
	}
	if _, err := cl.Get(ctx, box); err == nil {
		return box, nil
	}
	return "", fmt.Errorf("no box %q", box)
}

// shellQuote wraps a string in single quotes for safe use in a shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
