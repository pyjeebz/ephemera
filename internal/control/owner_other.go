//go:build !unix

package control

func matchDirOwner(string) error { return nil }
