package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/zacharytgray/yonder/internal/registry"
)

// ErrNoToken means the store has no token for that provider yet.
var ErrNoToken = errors.New("no token stored")

// TokenStore keeps a provider's access token off disk where it can.
// the account is the provider name.
type TokenStore interface {
	Kind() string
	Get(account string) (string, error)
	Set(account, token string) error
}

const (
	storeKeychain      = "keychain"
	storeSecretService = "secret-service"
	storeFile          = "file"
	keychainService    = "yonder"
	securityBin        = "/usr/bin/security"
)

// test hook
var newTokenStore = defaultTokenStore

// defaultTokenStore picks keychain on macOS, the secret service on linux
// desktops, else a 0600 file. kind overrides the pick.
func defaultTokenStore(kind string) TokenStore {
	if kind == "" {
		kind = storeFile
		if runtime.GOOS == "darwin" {
			if _, err := os.Stat(securityBin); err == nil {
				kind = storeKeychain
			}
		} else if _, err := lookPath("secret-tool"); err == nil && os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
			kind = storeSecretService
		}
	}
	switch kind {
	case storeKeychain:
		return keychain{}
	case storeSecretService:
		return secretService{}
	}
	return fileStore{dir: filepath.Join(registry.ConfigDir(), "tokens")}
}

// tokens go into `security -i` command lines, so keep them to plain characters
var tokenRe = regexp.MustCompile(`^[A-Za-z0-9._:=+/~-]+$`)

func checkToken(token string) error {
	if token == "" {
		return errors.New("empty token")
	}
	if len(token) > 8192 || !tokenRe.MatchString(token) {
		return errors.New("token has unexpected characters (spaces, quotes or newlines?); paste only the token")
	}
	return nil
}

// macOS keychain via /usr/bin/security.
type keychain struct{}

func (keychain) Kind() string { return "macOS Keychain" }

func (keychain) Get(account string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	out, _, err := run(ctx, securityBin, []string{"find-generic-password", "-s", keychainService, "-a", account, "-w"}, os.Environ(), nil)
	if exitCode(err) == 44 {
		return "", ErrNoToken
	}
	if err != nil {
		return "", fmt.Errorf("keychain lookup failed (exit %d); over ssh the login keychain may be locked: run `security unlock-keychain`, or set token_store = \"file\" under [secrets]", exitCode(err))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// Set runs `security -i` and writes the command on its stdin, so the token
// never shows up in any process's argv.
func (keychain) Set(account, token string) error {
	if err := checkToken(token); err != nil {
		return err
	}
	line := fmt.Sprintf("add-generic-password -U -s %s -a %s -l %s -w %s\n", keychainService, account, keychainService, token)
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	_, stderr, err := run(ctx, securityBin, []string{"-i"}, os.Environ(), []byte(line))
	if err != nil {
		msg := scrubbedLine(stderr, err, map[string]string{"TOKEN": token})
		return fmt.Errorf("keychain write failed: %s", msg)
	}
	return nil
}

// linux secret service via secret-tool, which reads the secret from stdin.
type secretService struct{}

func (secretService) Kind() string { return "secret service (secret-tool)" }

func (secretService) Get(account string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	out, stderr, err := run(ctx, "secret-tool", []string{"lookup", "service", keychainService, "account", account}, os.Environ(), nil)
	if err != nil && len(out) == 0 && len(strings.TrimSpace(string(stderr))) == 0 {
		return "", ErrNoToken
	}
	if err != nil {
		return "", fmt.Errorf("secret-tool lookup failed: %s; set token_store = \"file\" under [secrets] if no secret service runs here", lastLine(stderr))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func (secretService) Set(account, token string) error {
	if err := checkToken(token); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	_, stderr, err := run(ctx, "secret-tool", []string{"store", "--label=yonder " + account, "service", keychainService, "account", account}, os.Environ(), []byte(token))
	if err != nil {
		return fmt.Errorf("secret-tool store failed: %s", scrubbedLine(stderr, err, map[string]string{"TOKEN": token}))
	}
	return nil
}

// fileStore is the fallback: one 0600 file per provider.
type fileStore struct{ dir string }

func (f fileStore) Kind() string {
	return "plain file " + f.dir + " (0600; no keychain or secret service here)"
}

func (f fileStore) path(account string) string { return filepath.Join(f.dir, account) }

func (f fileStore) Get(account string) (string, error) {
	p := f.path(account)
	st, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoToken
	}
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by other users; run `chmod 600 %s`", p, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (f fileStore) Set(account, token string) error {
	if err := checkToken(token); err != nil {
		return err
	}
	return writeFile0600(f.path(account), []byte(token+"\n"))
}

// tokenFor prefers the provider's own env var (ci, wrappers), then the store.
func tokenFor(store TokenStore, account, envVar string) (token, source string, err error) {
	if v := os.Getenv(envVar); v != "" {
		return v, "the " + envVar + " environment variable", nil
	}
	t, err := store.Get(account)
	if err != nil {
		return "", store.Kind(), err
	}
	if t == "" {
		return "", store.Kind(), ErrNoToken
	}
	return t, store.Kind(), nil
}
