// Command eph-netadmin is the one privileged component in ephemera.
//
// It, and nothing else, carries CAP_NET_ADMIN. The daemon and CLI run with no
// capability at all and reach the network only by executing this: create a TAP,
// destroy a TAP, or report whether the capability is really here. Keeping the
// privilege on a binary this small is the point — the blast radius of a bug in
// the daemon stops well short of "can reconfigure the host's network", because
// the daemon cannot; only these few subcommands can, and they do exactly one
// thing each.
//
// It is granted the capability once, by build/host-setup.sh, as a file
// capability on the binary. There is no setuid and no root involved.
package main

import (
	"flag"
	"fmt"
	"net/netip"
	"os"

	"github.com/pyjeebz/ephemera/internal/vmnet"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "create":
		err = create(os.Args[2:])
	case "destroy":
		err = destroy(os.Args[2:])
	case "check":
		err = vmnet.CheckNetAdmin()
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "eph-netadmin: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "eph-netadmin:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: eph-netadmin <command> [flags]

  create   -tap NAME -addr HOST -mask NETMASK   create and bring up a TAP
  destroy  -tap NAME                            remove a TAP
  check                                         verify CAP_NET_ADMIN is present
`)
}

func create(argv []string) error {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	name := fs.String("tap", "", "interface name")
	addr := fs.String("addr", "", "host-end address")
	mask := fs.String("mask", "", "netmask, dotted quad")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	host, err := netip.ParseAddr(*addr)
	if err != nil {
		return fmt.Errorf("bad -addr: %w", err)
	}
	netmask, err := netip.ParseAddr(*mask)
	if err != nil {
		return fmt.Errorf("bad -mask: %w", err)
	}

	// Create then configure. On any failure remove the half-built device so the
	// caller's retry sees a clean slate rather than a name it cannot recreate.
	if err := vmnet.CreateTap(*name); err != nil {
		return err
	}
	if err := vmnet.ConfigureTap(*name, host, netmask); err != nil {
		_ = vmnet.RemoveTap(*name)
		return err
	}
	return nil
}

func destroy(argv []string) error {
	fs := flag.NewFlagSet("destroy", flag.ExitOnError)
	name := fs.String("tap", "", "interface name")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	return vmnet.RemoveTap(*name)
}
