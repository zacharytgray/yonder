package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/zacharytgray/yonder/internal/registry"
)

// provisioning: a person with a 1Password session here creates the vault and
// one service account per machine, read-only unless they allow writes. tokens
// stay in memory.

const (
	// DefaultVault is where agent secrets go unless --vault names another.
	DefaultVault = "Yonder Agents"
	// RevokeWhere is where service accounts are revoked; op can't do it.
	RevokeWhere = "https://start.1password.com/ > Developer > Service accounts"
	// ProvisionTokenEnv carries a new token to a target inside the _exec envelope.
	ProvisionTokenEnv = "YONDER_OP_TOKEN"

	vaultDescription      = "Secrets for yonder agents. Every machine's yonder service account can read all of it."
	extraVaultDescription = "Secrets that only the yonder service accounts given --extra-vault for it can read."
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

// CheckExtraVaults checks --extra-vault names and drops repeats and the
// agent vault itself.
func CheckExtraVaults(vault string, extras []string) ([]string, error) {
	var out []string
	for _, v := range extras {
		v = strings.TrimSpace(v)
		if err := CheckVaultName(v); err != nil {
			return nil, fmt.Errorf("--extra-vault: %w", err)
		}
		if v == vault || slices.Contains(out, v) {
			continue
		}
		out = append(out, v)
	}
	return out, nil
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
		// the app integration only answers op inside the desktop login session
		if os.Getenv("SSH_CONNECTION") != "" {
			return errors.New("op can't reach the 1Password app from an ssh session; run this in a terminal on the machine itself, with the app's CLI integration on")
		}
		return errors.New("op here can't reach a 1Password account; " + opDesktopFix + ". Run this from the machine you sit at")
	}
	return nil
}

// OpAndApp: op and the 1Password app are both installed. it runs nothing.
func OpAndApp() bool {
	_, err := lookPath("op")
	return err == nil && HasDesktopApp()
}

// EnsureVault makes the agent vault unless it exists. this is usually the
// call that asks for touch id; later calls reuse the approval.
func EnsureVault(name string) (created bool, err error) {
	return ensureVault(name, vaultDescription)
}

// EnsureExtraVault makes an extra vault unless it exists.
func EnsureExtraVault(name string) (created bool, err error) {
	return ensureVault(name, extraVaultDescription)
}

func ensureVault(name, description string) (created bool, err error) {
	bin, err := lookPath("op")
	if err != nil {
		return false, errors.New("1password CLI `op` not found; " + opInstallFix)
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	env := childEnv()
	out, stderr, err := run(ctx, bin, []string{"vault", "list", "--format", "json"}, env, nil)
	if err != nil {
		return false, fmt.Errorf("op vault list failed: %s (was the approval declined?)", scrubbedLine(stderr, err, env, nil))
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
	_, stderr, err = run(ctx, bin, []string{"vault", "create", name, "--description", description}, env, nil)
	if err != nil {
		return false, fmt.Errorf("op vault create failed: %s", scrubbedLine(stderr, err, env, nil))
	}
	return true, nil
}

// CreateServiceAccount makes a service account for vault, read-only unless
// write, that can also read each extra vault, and returns its token. the
// token is shown once; callers keep it in memory only.
func CreateServiceAccount(name, vault string, extras []string, expiresIn string, write bool) (string, error) {
	bin, err := lookPath("op")
	if err != nil {
		return "", errors.New("1password CLI `op` not found; " + opInstallFix)
	}
	perms := "read_items"
	if write {
		perms += ",write_items"
	}
	args := []string{"service-account", "create", name, "--vault", vault + ":" + perms}
	for _, v := range extras {
		args = append(args, "--vault", v+":read_items")
	}
	args = append(args, "--raw")
	if expiresIn != "" {
		args = append(args, "--expires-in", expiresIn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	env := childEnv()
	out, stderr, err := run(ctx, bin, args, env, nil)
	tok := strings.TrimSpace(string(out))
	if err != nil {
		msg := scrubbedLine(stderr, err, env, map[string]string{"TOKEN": tok})
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

// scrubbedLine is errLine after scrubbing all of stderr, so a value split
// over lines can't survive the cut. it scrubs vals and every provider
// credential in env, the child's environment: desktop-mode op inherits
// OP_SESSION_* and OP_CONNECT_TOKEN and can echo them.
func scrubbedLine(stderr []byte, err error, env []string, vals map[string]string) string {
	all := providerCreds(env)
	for k, v := range vals {
		all[k] = v
	}
	return errLine([]byte(ScrubText(string(stderr), all)), err)
}

// ProbeResult is what `yonder _secret-probe` reports about a machine. no values.
type ProbeResult struct {
	OS             string   `json:"os"`
	Op             string   `json:"op"` // path; empty when op is missing
	OpVersion      string   `json:"op_version"`
	DesktopApp     bool     `json:"desktop_app"`
	Provider       string   `json:"provider"`
	Mode           string   `json:"mode"`
	HasToken       bool     `json:"has_token"`
	ServiceAccount string   `json:"service_account"`
	Vault          string   `json:"vault"`
	ExtraVaults    []string `json:"extra_vaults"` // read-only vaults after the agent vault
	Write          bool     `json:"write"`        // the service account may write the vault
	Verified       bool     `json:"verified"`     // --verify: op accepted the stored token as a service account
	Detail         string   `json:"detail"`       // why verify failed
	// this yonder takes `_secret-provision --extra-vault`; older ones omit it
	CanExtraVaults bool `json:"can_extra_vaults"`
}

// Probe reports op, the provider settings and whether a token is stored.
// with verify it asks op who the stored token is.
func Probe(verify bool) ProbeResult {
	r := ProbeResult{OS: runtime.GOOS, DesktopApp: HasDesktopApp(), CanExtraVaults: true}
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
	r.Provider, r.Mode, r.ServiceAccount, r.Vault, r.Write = s.Provider, s.Mode, s.ServiceAccount, s.Vault, s.Write
	r.ExtraVaults = s.ExtraVaults
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
	env := childEnv(opEnvToken + "=" + tok)
	out, stderr, err := run(ctx, r.Op, []string{"user", "get", "--me", "--format", "json"}, env, nil)
	if err != nil {
		return false, "op rejected the token: " + scrubbedLine(stderr, err, env, nil)
	}
	if !strings.Contains(strings.ToUpper(string(out)), "SERVICE_ACCOUNT") {
		return false, "op accepted the token but it is not a service account"
	}
	return true, ""
}

// InstallServiceAccount stores token in this machine's token store and sets
// 1password service-account mode, recording the account, the vault, the
// extra vaults it reads and whether the account may write the vault.
func InstallServiceAccount(token, account, vault string, extras []string, write bool) (string, error) {
	if err := checkToken(token); err != nil {
		return "", err
	}
	cur, err := LoadSettings()
	if err != nil {
		return "", err
	}
	extras, err = CheckExtraVaults(vault, extras)
	if err != nil {
		return "", err
	}
	s := Settings{Provider: Provider1Password, Mode: ModeServiceAccount, TokenStore: cur.TokenStore, ServiceAccount: account, Vault: vault, ExtraVaults: extras, Write: write}
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
