package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ephemera.run/ephemera/internal/auth"
)

// tokenEnv overrides the stored sender token (CI, scripts).
const tokenEnv = "EPHEMERA_TOKEN"

// tokenFile is where `ephemera login` keeps the sender token.
func tokenFile() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "token"), nil
}

// senderToken is the token send and resume present to the relay:
// EPHEMERA_TOKEN if set, else the stored login, else none (fine on
// an open relay; a relay that needs one refuses with a reason).
func senderToken() (string, error) {
	if t := os.Getenv(tokenEnv); t != "" {
		return t, nil
	}
	path, err := tokenFile()
	if err != nil {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// runLogin stores a sender token. It reads the token from stdin, not
// the command line, so it stays out of shell history and ps.
func runLogin(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: ephemera login   (paste the token, or pipe it in)")
	}
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprint(os.Stderr, "sender token: ")
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	token := strings.TrimSpace(line)
	if token == "" && err != nil {
		return errors.New("no token read from stdin")
	}
	if !strings.HasPrefix(token, auth.TokenPrefix) {
		return fmt.Errorf("that does not look like a sender token (they start with %q)", auth.TokenPrefix)
	}

	path, err := tokenFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "logged in: token saved to %s\n", path)
	return nil
}

// runLogout forgets the stored sender token.
func runLogout(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: ephemera logout")
	}
	path, err := tokenFile()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(os.Stderr, "logged out")
	return nil
}

func init() {
	register("login", runLogin)
	register("logout", runLogout)
}
