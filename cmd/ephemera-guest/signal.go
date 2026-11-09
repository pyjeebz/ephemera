//go:build linux

package main

import (
	"os/signal"
	"syscall"
)

func signalIgnore(sig syscall.Signal) { signal.Ignore(sig) }
