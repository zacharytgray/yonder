package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/zacharytgray/yonder/internal/registry"
)

// ErrNoProvider means config.toml has no [secrets] provider yet.
var ErrNoProvider = errors.New("no secrets provider configured; run `yonder secret setup`")

const (
	Provider1Password = "1password"
	ProviderBWS       = "bws"

	ModeDesktop        = "desktop"
	ModeServiceAccount = "service-account"
)

// Provider turns references into values. it never logs or prints a value.
type Provider interface {
	Name() string
	Resolve(refs map[string]string) (map[string]string, error)
	Health() []Check
}

// Check is one doctor line about secrets on this machine.
type Check struct {
	Name   string
	OK     bool
	Detail string
	Fix    string // command or step that fixes it; empty when OK
}

// Settings is the [secrets] table in config.toml.
type Settings struct {
	Provider   string // 1password | bws
	Mode       string // 1password only: desktop | service-account
	TokenStore string // optional override: keychain | secret-service | file
	// service-account mode only, set by provisioning: which account the token
	// belongs to and the vault it reads, so doctor and remove --lost can name it
	ServiceAccount string
	Vault          string
}

// HandsFree is true when the provider runs on a stored token, not a person.
func (s Settings) HandsFree() bool {
	return s.Provider == ProviderBWS || (s.Provider == Provider1Password && s.Mode == ModeServiceAccount)
}

func (s Settings) String() string {
	if s.Provider == Provider1Password {
		return s.Provider + " (" + s.Mode + ")"
	}
	return s.Provider
}

func LoadSettings() (Settings, error) {
	c, err := registry.Open().Config()
	if err != nil {
		return Settings{}, err
	}
	str := func(k string) string {
		v, _ := c.Secrets[k].(string)
		return strings.TrimSpace(v)
	}
	s := Settings{Provider: str("provider"), Mode: str("mode"), TokenStore: str("token_store")}
	if s.Provider == Provider1Password && s.Mode == "" {
		s.Mode = ModeDesktop
	}
	if s.Provider == Provider1Password && s.Mode == ModeServiceAccount {
		s.ServiceAccount, s.Vault = str("service_account"), str("vault")
	}
	return s, nil
}

func SaveSettings(s Settings) error {
	t := map[string]any{}
	if s.Provider != "" {
		t["provider"] = s.Provider
	}
	if s.Provider == Provider1Password && s.Mode != "" {
		t["mode"] = s.Mode
	}
	if s.TokenStore != "" {
		t["token_store"] = s.TokenStore
	}
	if s.Provider == Provider1Password && s.Mode == ModeServiceAccount {
		if s.ServiceAccount != "" {
			t["service_account"] = s.ServiceAccount
		}
		if s.Vault != "" {
			t["vault"] = s.Vault
		}
	}
	return registry.Open().SetSecrets(t)
}

func validateSettings(s Settings) error {
	switch s.Provider {
	case Provider1Password:
		if s.Mode != ModeDesktop && s.Mode != ModeServiceAccount {
			return fmt.Errorf("unknown 1password mode %q (want desktop or service-account)", s.Mode)
		}
	case ProviderBWS:
	default:
		return fmt.Errorf("unknown secrets provider %q (want 1password or bws)", s.Provider)
	}
	switch s.TokenStore {
	case "", storeKeychain, storeSecretService, storeFile:
	default:
		return fmt.Errorf("unknown token_store %q (want keychain, secret-service or file)", s.TokenStore)
	}
	return nil
}

func newProvider(s Settings) (Provider, error) {
	if s.Provider == "" {
		return nil, ErrNoProvider
	}
	if err := validateSettings(s); err != nil {
		return nil, err
	}
	if s.Provider == ProviderBWS {
		return &bws{tokens: newTokenStore(s.TokenStore)}, nil
	}
	return &onePassword{mode: s.Mode, tokens: newTokenStore(s.TokenStore), account: s.ServiceAccount}, nil
}

// names

var (
	nameRe   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	bwsRefRe = regexp.MustCompile(`^bws://[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// ValidName checks a secret name: it becomes an env var on the target.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("bad secret name %q (want an env var name like API_KEY: [A-Z_][A-Z0-9_]*)", name)
	}
	return nil
}

// ValidRef checks a reference's syntax for whichever provider its scheme names.
func ValidRef(ref string) error {
	switch {
	case strings.HasPrefix(ref, "op://"):
		return validOpRef(ref)
	case strings.HasPrefix(ref, "bws://"):
		if !bwsRefRe.MatchString(ref) {
			return fmt.Errorf("bad bws reference %q (want bws://<secret uuid>)", ref)
		}
		return nil
	}
	return fmt.Errorf("bad reference %q (want op://vault/item/field or bws://<secret uuid>)", ref)
}

// validOpRef: op://vault/item/[section/]field, optional ?attribute=...
func validOpRef(ref string) error {
	bad := fmt.Errorf("bad 1password reference %q (want op://vault/item/field or op://vault/item/section/field)", ref)
	if strings.ContainsAny(ref, "{}$\n\r\t\"'`\\") {
		return bad
	}
	path, _, _ := strings.Cut(strings.TrimPrefix(ref, "op://"), "?")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || len(parts) > 4 {
		return bad
	}
	for _, p := range parts {
		if p == "" || strings.TrimSpace(p) != p {
			return bad
		}
	}
	return nil
}

func refProvider(ref string) string {
	if strings.HasPrefix(ref, "bws://") {
		return ProviderBWS
	}
	if strings.HasPrefix(ref, "op://") {
		return Provider1Password
	}
	return ""
}

// secrets.toml: NAME = "ref", one per machine, never synced

func RefsPath() string { return filepath.Join(registry.ConfigDir(), "secrets.toml") }

// LoadRefs reads secrets.toml. a missing file is an empty map.
func LoadRefs() (map[string]string, error) {
	refs := map[string]string{}
	data, err := os.ReadFile(RefsPath())
	if errors.Is(err, os.ErrNotExist) {
		return refs, nil
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, &refs); err != nil {
		return nil, fmt.Errorf("%s: %w (want NAME = \"op://...\" lines)", RefsPath(), err)
	}
	return refs, nil
}

func saveRefs(refs map[string]string) error {
	data, err := toml.Marshal(refs)
	if err != nil {
		return err
	}
	header := "# secret references for this machine, never values. edit with `yonder secret add/rm`.\n"
	return writeFile0600(RefsPath(), append([]byte(header), data...))
}

func AddRef(name, ref string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if err := ValidRef(ref); err != nil {
		return err
	}
	refs, err := LoadRefs()
	if err != nil {
		return err
	}
	refs[name] = ref
	return saveRefs(refs)
}

func RemoveRef(name string) error {
	refs, err := LoadRefs()
	if err != nil {
		return err
	}
	if _, ok := refs[name]; !ok {
		return unknownErr([]string{name}, refs)
	}
	delete(refs, name)
	return saveRefs(refs)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func unknownErr(names []string, refs map[string]string) error {
	known := "none yet"
	if len(refs) > 0 {
		known = strings.Join(sortedKeys(refs), ", ")
	}
	word := "secret"
	if len(names) > 1 {
		word = "secrets"
	}
	return fmt.Errorf("unknown %s %s on this machine (known: %s); add one with `yonder secret add NAME <ref>`", word, strings.Join(names, ", "), known)
}

// Resolve turns secret names into values with this machine's provider, in one provider call.
func Resolve(names []string) (map[string]string, error) {
	if len(names) == 0 {
		return map[string]string{}, nil
	}
	s, err := LoadSettings()
	if err != nil {
		return nil, err
	}
	p, err := newProvider(s)
	if err != nil {
		return nil, err
	}
	refs, err := LoadRefs()
	if err != nil {
		return nil, err
	}
	return resolveWith(p, names, refs)
}

func resolveWith(p Provider, names []string, refs map[string]string) (map[string]string, error) {
	pick := map[string]string{}
	var unknown []string
	for _, n := range names {
		if err := ValidName(n); err != nil {
			return nil, err
		}
		ref, ok := refs[n]
		if !ok {
			unknown = append(unknown, n)
			continue
		}
		if err := ValidRef(ref); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if rp := refProvider(ref); rp != p.Name() {
			return nil, fmt.Errorf("%s is a %s reference but this machine's provider is %s; re-add it with `yonder secret add %s <ref>`", n, rp, p.Name(), n)
		}
		pick[n] = ref
	}
	if len(unknown) > 0 {
		return nil, unknownErr(unknown, refs)
	}
	vals, err := p.Resolve(pick)
	if err != nil {
		return nil, err
	}
	for n := range pick {
		if _, ok := vals[n]; !ok {
			return nil, fmt.Errorf("%s: provider returned no value", n)
		}
	}
	return vals, nil
}

// Health reports whether this machine's secrets setup works. it never
// resolves or prints a value.
func Health() []Check {
	s, err := LoadSettings()
	if err != nil {
		return []Check{{Name: "secrets config", Detail: err.Error(), Fix: "fix the [secrets] table in config.toml, or run `yonder secret setup`"}}
	}
	refs, rerr := LoadRefs()
	var out []Check
	if s.Provider == "" {
		if rerr == nil && len(refs) == 0 {
			return []Check{{Name: "secrets provider", OK: true, Detail: "not set up (optional); run `yonder secret setup` to use `yonder run --secret`"}}
		}
		out = append(out, Check{Name: "secrets provider", Detail: "secrets.toml has references but no provider is configured", Fix: "yonder secret setup"})
	} else if p, err := newProvider(s); err != nil {
		out = append(out, Check{Name: "secrets provider", Detail: err.Error(), Fix: "yonder secret setup"})
	} else {
		out = append(out, Check{Name: "secrets provider", OK: true, Detail: s.String()})
		out = append(out, p.Health()...)
	}
	return append(out, refChecks(s.Provider, refs, rerr)...)
}

func refChecks(provider string, refs map[string]string, err error) []Check {
	if err != nil {
		return []Check{{Name: "secret references", Detail: err.Error(), Fix: "fix or delete " + RefsPath() + ", then `yonder secret add NAME <ref>`"}}
	}
	if len(refs) == 0 {
		return []Check{{Name: "secret references", OK: true, Detail: "none yet; add one with `yonder secret add NAME <ref>`"}}
	}
	var out []Check
	for _, n := range sortedKeys(refs) {
		ref := refs[n]
		c := Check{Name: "secret " + n}
		switch {
		case ValidName(n) != nil:
			c.Detail = ValidName(n).Error()
			c.Fix = fmt.Sprintf("yonder secret rm %s, then add it under an env var name", n)
		case ValidRef(ref) != nil:
			c.Detail = ValidRef(ref).Error()
			c.Fix = fmt.Sprintf("yonder secret add %s %s", n, exampleRef(provider))
		case provider != "" && refProvider(ref) != provider:
			c.Detail = fmt.Sprintf("%s reference, but this machine uses %s", refProvider(ref), provider)
			c.Fix = fmt.Sprintf("yonder secret add %s %s", n, exampleRef(provider))
		default:
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		out = append(out, Check{Name: "secret references", OK: true, Detail: fmt.Sprintf("%d valid: %s", len(refs), strings.Join(sortedKeys(refs), ", "))})
	}
	return out
}

func exampleRef(provider string) string {
	if provider == ProviderBWS {
		return "bws://<secret uuid>"
	}
	return "op://<vault>/<item>/<field>"
}

func writeFile0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp, 0o600)); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
