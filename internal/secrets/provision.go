package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"

	"github.com/zacharytgray/yonder/internal/registry"
)

// provisioning: a person with a 1Password session here creates the vault and
// one read-only service account per machine. tokens stay in memory.

const (
	// DefaultVault is where agent secrets go unless --vault names another.
	DefaultVault = "Yonder Agents"
	// RevokeWhere is where service accounts are revoked; op can't do it.
	RevokeWhere = "https://start.1password.com/ > Developer > Service accounts"
	// ProvisionTokenEnv carries a new token to a target inside the _exec envelope.
	ProvisionTokenEnv = "YONDER_OP_TOKEN"

	vaultDescription = "Secrets for yonder agents. Every machine's yonder service account can read all of it."
)

// AccountName is the service account a machine gets.
func AccountName(machine string) string { return "yonder-" + machine }

// OpInstallHint says how to install op on goos.
func OpInstallHint(goos string) string {
	if goos == "darwin" {
		return "brew install 1password-cli"
	}
	return "see https://developer.1password.com/docs/cli/get-started/"
}

// service accounts can't be granted these
var reservedVaults = []string{"personal", "private", "employee", "shared"}

// CheckVaultName refuses names a service account can't use or op would misparse.
func CheckVaultName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("empty vault name")
	}
	if strings.ContainsAny(name, ":,\n\r\t") {
		return fmt.Errorf("vault name %q can't hold ':' ',' or control characters", name)
	}
	for _, r := range reservedVaults {
		if strings.EqualFold(strings.TrimSpace(name), r) {
			return fmt.Errorf("1Password service accounts can't read the %s vault; use a vault just for agents, like %q", name, DefaultVault)
		}
	}
	return nil
}

var expiresRe = regexp.MustCompile(`^([0-9]+[smhdw])+$`)

// CheckExpiresIn checks an --expires-in value op accepts, like 30d or 12h.
func CheckExpiresIn(s string) error {
	if !expiresRe.MatchString(s) {
		return fmt.Errorf("bad --expires-in %q (want a duration like 30d, 2w or 12h)", s)
	}
	return nil
}

// UserSession checks that op here can act as a signed-in person (the app
// integration or `op signin`), which creating service accounts needs.
// it reads nothing from the vaults.
func UserSession() error {
	bin, err := lookPath("op")
	if err != nil {
		return fmt.Errorf("the 1Password CLI `op` is not installed here; install it (%s), then turn on the app's CLI integration", OpInstallHint(runtime.GOOS))
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	out, _, err := run(ctx, bin, []string{"account", "list", "--format", "json"}, childEnv(), nil)
	var list []json.RawMessage
	if err != nil || json.Unmarshal(out, &list) != nil || len(list) == 0 {
		return errors.New("op here can't reach a 1Password account; " + opDesktopFix + ". Run this from the machine you sit at")
	}
	return nil
}

// OpAndApp: op and the 1Password app are both installed. it runs nothing.
func OpAndApp() bool {
	_, err := lookPath("op")
	return err == nil && HasDesktopApp()
}

// EnsureVault makes the vault unless it exists. this is usually the call that
// asks for touch id; later calls reuse the approval.
func EnsureVault(name string) (created bool, err error) {
	bin, err := lookPath("op")
	if err != nil {
		return false, errors.New("1password CLI `op` not found; " + opInstallFix)
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	out, stderr, err := run(ctx, bin, []string{"vault", "list", "--format", "json"}, childEnv(), nil)
	if err != nil {
		return false, fmt.Errorf("op vault list failed: %s (was the approval declined?)", errLine(stderr, err))
	}
	var vaults []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &vaults); err != nil {
		return false, fmt.Errorf("op vault list returned output yonder could not parse: %w", err)
	}
	for _, v := range vaults {
		if v.Name == name {
			return false, nil
		}
	}
	_, stderr, err = run(ctx, bin, []string{"vault", "create", name, "--description", vaultDescription}, childEnv(), nil)
	if err != nil {
		return false, fmt.Errorf("op vault create failed: %s", errLine(stderr, err))
	}
	return true, nil
}

// CreateServiceAccount makes a read-only service account for vault and
// returns its token. the token is shown once; callers keep it in memory only.
func CreateServiceAccount(name, vault, expiresIn string) (string, error) {
	bin, err := lookPath("op")
	if err != nil {
		return "", errors.New("1password CLI `op` not found; " + opInstallFix)
	}
	args := []string{"service-account", "create", name, "--vault", vault + ":read_items", "--raw"}
	if expiresIn != "" {
		args = append(args, "--expires-in", expiresIn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	out, stderr, err := run(ctx, bin, args, childEnv(), nil)
	tok := strings.TrimSpace(string(out))
	if err != nil {
		msg := ScrubText(errLine(stderr, err), map[string]string{"TOKEN": tok})
		return "", fmt.Errorf("op service-account create failed: %s", msg)
	}
	if checkToken(tok) != nil {
		return "", errors.New("op service-account create printed something other than a token (not shown)")
	}
	return tok, nil
}

func errLine(stderr []byte, err error) string {
	if l := lastLine(stderr); l != "" {
		return l
	}
	return err.Error()
}

// ProbeResult is what `yonder _secret-probe` reports about a machine. no values.
type ProbeResult struct {
	OS             string `json:"os"`
	Op             string `json:"op"` // path; empty when op is missing
	OpVersion      string `json:"op_version"`
	DesktopApp     bool   `json:"desktop_app"`
	Provider       string `json:"provider"`
	Mode           string `json:"mode"`
	HasToken       bool   `json:"has_token"`
	ServiceAccount string `json:"service_account"`
	Vault          string `json:"vault"`
	Verified       bool   `json:"verified"` // --verify: op accepted the stored token as a service account
	Detail         string `json:"detail"`   // why verify failed
}

// Probe reports op, the provider settings and whether a token is stored.
// with verify it asks op who the stored token is.
func Probe(verify bool) ProbeResult {
	r := ProbeResult{OS: runtime.GOOS, DesktopApp: HasDesktopApp()}
	if bin, err := lookPath("op"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		ver, _, err := run(ctx, bin, []string{"--version"}, childEnv(), nil)
		cancel()
		if err == nil {
			r.Op, r.OpVersion = bin, strings.TrimSpace(string(ver))
		}
	}
	s, err := LoadSettings()
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	r.Provider, r.Mode, r.ServiceAccount, r.Vault = s.Provider, s.Mode, s.ServiceAccount, s.Vault
	var tok string
	if s.HandsFree() {
		envVar := opEnvToken
		if s.Provider == ProviderBWS {
			envVar = bwsEnvToken
		}
		t, _, err := tokenFor(newTokenStore(s.TokenStore), s.Provider, envVar)
		if err == nil {
			r.HasToken, tok = true, t
		} else if !errors.Is(err, ErrNoToken) {
			r.Detail = err.Error()
		}
	}
	if verify {
		r.Verified, r.Detail = verifyServiceAccount(r, s, tok)
	}
	return r
}

func verifyServiceAccount(r ProbeResult, s Settings, tok string) (bool, string) {
	switch {
	case r.Op == "":
		return false, "op is not installed"
	case s.Provider != Provider1Password || s.Mode != ModeServiceAccount:
		return false, "this machine is set to " + s.String() + ", not 1password (service-account)"
	case tok == "":
		if r.Detail != "" {
			return false, r.Detail
		}
		return false, "no token stored"
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	out, stderr, err := run(ctx, r.Op, []string{"user", "get", "--me", "--format", "json"}, childEnv(opEnvToken+"="+tok), nil)
	if err != nil {
		return false, "op rejected the token: " + ScrubText(errLine(stderr, err), map[string]string{"TOKEN": tok})
	}
	if !strings.Contains(strings.ToUpper(string(out)), "SERVICE_ACCOUNT") {
		return false, "op accepted the token but it is not a service account"
	}
	return true, ""
}

// InstallServiceAccount stores token in this machine's token store and sets
// 1password service-account mode, recording the account and vault names.
func InstallServiceAccount(token, account, vault string) (string, error) {
	if err := checkToken(token); err != nil {
		return "", err
	}
	cur, err := LoadSettings()
	if err != nil {
		return "", err
	}
	s := Settings{Provider: Provider1Password, Mode: ModeServiceAccount, TokenStore: cur.TokenStore, ServiceAccount: account, Vault: vault}
	if err := validateSettings(s); err != nil {
		return "", err
	}
	store := newTokenStore(s.TokenStore)
	if err := store.Set(Provider1Password, token); err != nil {
		return "", err
	}
	if err := SaveSettings(s); err != nil {
		return "", err
	}
	return store.Kind(), nil
}

func selfOrPlaceholder() string {
	if c, err := registry.Open().Config(); err == nil && c.Self != "" {
		return c.Self
	}
	return "<this machine>"
}
