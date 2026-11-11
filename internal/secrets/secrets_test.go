package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type call struct {
	bin   string
	args  []string
	env   []string
	stdin string
}

// fakeExec swaps run and lookPath. handler answers each call.
type fakeExec struct {
	calls   []call
	handler func(c call) (stdout, stderr string, err error)
	missing map[string]bool
}

func installFake(t *testing.T, f *fakeExec) {
	t.Helper()
	oldRun, oldLook := run, lookPath
	run = func(_ context.Context, bin string, args, env []string, stdin []byte) ([]byte, []byte, error) {
		c := call{bin: bin, args: args, env: env, stdin: string(stdin)}
		f.calls = append(f.calls, c)
		out, errOut, err := f.handler(c)
		return []byte(out), []byte(errOut), err
	}
	lookPath = func(name string) (string, error) {
		if f.missing[name] {
			return "", errors.New("not found")
		}
		return "/fake/" + name, nil
	}
	t.Cleanup(func() { run, lookPath = oldRun, oldLook })
}

type fakeTokens struct {
	tokens map[string]string
	err    error
}

func (f *fakeTokens) Kind() string { return "fake store" }
func (f *fakeTokens) Get(a string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	t, ok := f.tokens[a]
	if !ok {
		return "", ErrNoToken
	}
	return t, nil
}
func (f *fakeTokens) Set(a, t string) error { f.tokens[a] = t; return nil }

func installTokens(t *testing.T, ft *fakeTokens) {
	t.Helper()
	old := newTokenStore
	newTokenStore = func(string) TokenStore { return ft }
	t.Cleanup(func() { newTokenStore = old })
}

// fakeInject plays op inject: swaps each {{ ref }} for vals[ref].
func fakeInject(vals map[string]string) func(c call) (string, string, error) {
	re := regexp.MustCompile(`\{\{ (op://[^}]+) \}\}`)
	return func(c call) (string, string, error) {
		switch {
		case len(c.args) == 1 && c.args[0] == "inject":
			var missing error
			out := re.ReplaceAllStringFunc(c.stdin, func(m string) string {
				ref := re.FindStringSubmatch(m)[1]
				v, ok := vals[ref]
				if !ok {
					missing = fmt.Errorf("no item for %s", ref)
				}
				return v
			})
			if missing != nil {
				return "", "[ERROR] 2026/11/11 20:00:00 " + missing.Error() + "\n", errors.New("exit status 1")
			}
			return out, "", nil
		case len(c.args) == 1 && c.args[0] == "--version":
			return "2.34.0\n", "", nil
		}
		return "", "", errors.New("exit status 1")
	}
}

func setupHome(t *testing.T, s Settings, refs map[string]string) {
	t.Helper()
	t.Setenv("YONDER_HOME", t.TempDir())
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	t.Setenv("BWS_ACCESS_TOKEN", "")
	if s.Provider != "" {
		if err := SaveSettings(s); err != nil {
			t.Fatal(err)
		}
	}
	for n, r := range refs {
		if err := AddRef(n, r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveHappyPathOneInjectCall(t *testing.T) {
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeDesktop}, map[string]string{
		"API_KEY": "op://example-vault/api/credential",
		"DB_PASS": "op://example-vault/db/main/password",
		"UNUSED":  "op://example-vault/other/password",
	})
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "ops_should-be-stripped")
	vals := map[string]string{
		"op://example-vault/api/credential":   "sk-example-123",
		"op://example-vault/db/main/password": "line one\nline two\n",
	}
	f := &fakeExec{handler: fakeInject(vals)}
	installFake(t, f)
	got, err := Resolve([]string{"API_KEY", "DB_PASS"})
	if err != nil {
		t.Fatal(err)
	}
	if got["API_KEY"] != "sk-example-123" || got["DB_PASS"] != "line one\nline two\n" || len(got) != 2 {
		t.Fatalf("got %q", got)
	}
	if len(f.calls) != 1 {
		t.Fatalf("want one op call, got %d", len(f.calls))
	}
	c := f.calls[0]
	if strings.Join(c.args, " ") != "inject" {
		t.Fatalf("argv: %v", c.args)
	}
	for _, kv := range c.env {
		if strings.HasPrefix(kv, "OP_SERVICE_ACCOUNT_TOKEN=") {
			t.Fatal("desktop mode must not pass a service account token")
		}
	}
	if strings.Contains(c.stdin, "UNUSED") {
		t.Fatal("only requested names go to op")
	}
}

func TestResolveServiceAccountTokenOnlyInEnv(t *testing.T) {
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeServiceAccount}, map[string]string{"API_KEY": "op://example-vault/api/credential"})
	installTokens(t, &fakeTokens{tokens: map[string]string{"1password": "ops_example-token"}})
	f := &fakeExec{handler: fakeInject(map[string]string{"op://example-vault/api/credential": "v"})}
	installFake(t, f)
	if _, err := Resolve([]string{"API_KEY"}); err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	if strings.Contains(strings.Join(c.args, " "), "ops_example-token") || strings.Contains(c.stdin, "ops_example-token") {
		t.Fatal("token leaked into argv or stdin")
	}
	found := false
	for _, kv := range c.env {
		found = found || kv == "OP_SERVICE_ACCOUNT_TOKEN=ops_example-token"
	}
	if !found {
		t.Fatal("token should be in the child env")
	}
}

func TestResolveUnknownName(t *testing.T) {
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeDesktop}, map[string]string{"API_KEY": "op://a/b/c", "OTHER": "op://a/b/d"})
	f := &fakeExec{handler: fakeInject(nil)}
	installFake(t, f)
	_, err := Resolve([]string{"API_KEY", "NOPE"})
	if err == nil || !strings.Contains(err.Error(), "unknown secret NOPE") || !strings.Contains(err.Error(), "known: API_KEY, OTHER") {
		t.Fatalf("got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatal("no provider call when a name is unknown")
	}
}

func TestResolveProviderMismatch(t *testing.T) {
	setupHome(t, Settings{Provider: ProviderBWS}, map[string]string{"API_KEY": "op://a/b/c"})
	installFake(t, &fakeExec{handler: fakeInject(nil)})
	if _, err := Resolve([]string{"API_KEY"}); err == nil || !strings.Contains(err.Error(), "provider is bws") {
		t.Fatalf("got %v", err)
	}
}

func TestValueNeverInErrors(t *testing.T) {
	const secret = "sk-very-secret-987"
	refs := map[string]string{"API_KEY": "op://a/b/c", "B": "op://a/b/d"}
	cases := map[string]func(c call) (string, string, error){
		"garbled output": func(c call) (string, string, error) { return "junk " + secret + " junk", "", nil },
		"missing block": func(c call) (string, string, error) {
			out := fakeInject(map[string]string{"op://a/b/c": secret, "op://a/b/d": "x"})
			o, _, _ := out(c)
			return strings.Replace(o, "B\n", "C\n", 1), "", nil
		},
		"failed with partial output": func(c call) (string, string, error) {
			return secret, "[ERROR] 2026/11/11 20:00:00 rate limited", errors.New("exit status 1")
		},
	}
	for name, h := range cases {
		installFake(t, &fakeExec{handler: h})
		p := &onePassword{mode: ModeDesktop, tokens: &fakeTokens{}}
		_, err := resolveWith(p, []string{"API_KEY", "B"}, refs)
		if err == nil {
			t.Fatalf("%s: want an error", name)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%s: value leaked: %v", name, err)
		}
	}

	// bws: bad json that holds the value
	installFake(t, &fakeExec{handler: func(c call) (string, string, error) {
		return `{"key":"API_KEY","value":"` + secret, "", nil
	}})
	b := &bws{tokens: &fakeTokens{tokens: map[string]string{"bws": "0.tok"}}}
	if _, err := b.Resolve(map[string]string{"API_KEY": "bws://11111111-2222-3333-4444-555555555555"}); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("bws: %v", err)
	}

	// token: a store failure that echoes the token is scrubbed
	installFake(t, &fakeExec{handler: func(c call) (string, string, error) {
		return "", "security: bad input near " + secret, errors.New("exit status 1")
	}})
	if err := (keychain{}).Set("1password", secret); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("keychain: %v", err)
	}
}

func TestInjectTemplateRoundTrip(t *testing.T) {
	refs := map[string]string{"A": "op://v/i/f", "B_2": "op://v/i/s/f"}
	tmpl, boundary := injectTemplate(refs)
	s := string(tmpl)
	if !strings.Contains(s, "{{ op://v/i/f }}") || !strings.Contains(s, "{{ op://v/i/s/f }}") || strings.Count(s, boundary) != 3 {
		t.Fatalf("template: %q", s)
	}
	out := strings.NewReplacer("{{ op://v/i/f }}", "", "{{ op://v/i/s/f }}", "multi\nline\n").Replace(s)
	got, err := parseInject([]byte(out), boundary, refs)
	if err != nil {
		t.Fatal(err)
	}
	if got["A"] != "" || got["B_2"] != "multi\nline\n" || len(got) != 2 {
		t.Fatalf("got %q", got)
	}
	if _, b2 := injectTemplate(refs); b2 == boundary {
		t.Fatal("boundary should be random")
	}
	for _, bad := range []string{"", "nothing", strings.Replace(out, boundary+" end", "", 1), strings.Replace(out, "A\n", "Z\n", 1)} {
		if _, err := parseInject([]byte(bad), boundary, refs); err == nil {
			t.Fatalf("should reject %q", bad)
		}
	}
}

func TestParseBWS(t *testing.T) {
	v, err := parseBWS([]byte(`{"id":"11111111-2222-3333-4444-555555555555","key":"API_KEY","value":"abc\ndef","note":""}`))
	if err != nil || v != "abc\ndef" {
		t.Fatalf("%q %v", v, err)
	}
	for _, bad := range []string{"", "not json", `{"key":"x"}`, `[1]`} {
		if _, err := parseBWS([]byte(bad)); err == nil {
			t.Fatalf("should reject %q", bad)
		}
	}
}

func TestBWSResolve(t *testing.T) {
	setupHome(t, Settings{Provider: ProviderBWS}, map[string]string{
		"A": "bws://11111111-2222-3333-4444-555555555555",
		"B": "bws://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	})
	installTokens(t, &fakeTokens{tokens: map[string]string{"bws": "0.example-token"}})
	f := &fakeExec{handler: func(c call) (string, string, error) {
		return `{"key":"k","value":"val-` + c.args[2][:4] + `"}`, "", nil
	}}
	installFake(t, f)
	got, err := Resolve([]string{"A", "B"})
	if err != nil || got["A"] != "val-1111" || got["B"] != "val-aaaa" {
		t.Fatalf("%q %v", got, err)
	}
	if strings.Join(f.calls[0].args, " ") != "secret get 11111111-2222-3333-4444-555555555555 --output json" {
		t.Fatalf("argv: %v", f.calls[0].args)
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.args, " "), "example-token") {
			t.Fatal("token in argv")
		}
	}
}

func TestValidation(t *testing.T) {
	for _, n := range []string{"API_KEY", "_X", "A1"} {
		if ValidName(n) != nil {
			t.Fatalf("%s should be valid", n)
		}
	}
	for _, n := range []string{"", "api_key", "1A", "A-B", "A B"} {
		if ValidName(n) == nil {
			t.Fatalf("%q should be invalid", n)
		}
	}
	for _, r := range []string{"op://v/i/f", "op://example-vault/My Item/section/field", "op://v/i/f?attribute=otp", "bws://11111111-2222-3333-4444-555555555555"} {
		if err := ValidRef(r); err != nil {
			t.Fatalf("%s: %v", r, err)
		}
	}
	for _, r := range []string{"", "op://v/i", "op://v//f", "op://v/i/f/g/h", "op://v/i/{{f}}", "op://$VAULT/i/f", "op://v/i/f\n", "bws://not-a-uuid", "vault/item/field", "https://example.com/x"} {
		if ValidRef(r) == nil {
			t.Fatalf("%q should be invalid", r)
		}
	}
	if AddRef("bad-name", "op://v/i/f") == nil || AddRef("OK", "nope") == nil {
		t.Fatal("AddRef must validate")
	}
}

func TestRefsFileIsPrivate(t *testing.T) {
	setupHome(t, Settings{}, map[string]string{"A": "op://v/i/f"})
	st, err := os.Stat(RefsPath())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st.Mode(), err)
	}
	if err := RemoveRef("A"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRef("A"); err == nil || !strings.Contains(err.Error(), "unknown secret A") {
		t.Fatalf("got %v", err)
	}
}

func checkFix(t *testing.T, checks []Check, name, fixPart string) {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			if c.OK || !strings.Contains(c.Fix, fixPart) {
				t.Fatalf("%s: %+v", name, c)
			}
			return
		}
	}
	t.Fatalf("no %s check in %+v", name, checks)
}

func TestHealthFixes(t *testing.T) {
	// unused secrets are fine
	setupHome(t, Settings{}, nil)
	if h := Health(); len(h) != 1 || !h[0].OK {
		t.Fatalf("%+v", h)
	}

	// references but no provider
	setupHome(t, Settings{}, map[string]string{"A": "op://v/i/f"})
	checkFix(t, Health(), "secrets provider", "yonder secret setup")

	// op missing
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeDesktop}, map[string]string{"A": "op://v/i/f"})
	installFake(t, &fakeExec{handler: fakeInject(nil), missing: map[string]bool{"op": true}})
	checkFix(t, Health(), "1password cli", "developer.1password.com")

	// desktop: whoami fails and the app shows no accounts
	installFake(t, &fakeExec{handler: func(c call) (string, string, error) {
		switch c.args[0] {
		case "--version":
			return "2.34.0\n", "", nil
		case "account":
			return "[]", "", nil
		}
		return "", "", errors.New("exit status 1")
	}})
	checkFix(t, Health(), "1password app integration", "Integrate with 1Password CLI")

	// desktop: integration on, not yet authorized: ok
	installFake(t, &fakeExec{handler: func(c call) (string, string, error) {
		switch c.args[0] {
		case "--version":
			return "2.34.0\n", "", nil
		case "account":
			return `[{"url":"example.1password.com"}]`, "", nil
		}
		return "", "", errors.New("exit status 1")
	}})
	for _, c := range Health() {
		if !c.OK {
			t.Fatalf("want all ok: %+v", c)
		}
	}

	// service account without a token
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeServiceAccount}, nil)
	if err := saveRefs(map[string]string{"A": "op://v/i/f", "B": "bws://x"}); err != nil {
		t.Fatal(err)
	}
	installTokens(t, &fakeTokens{tokens: map[string]string{}})
	installFake(t, &fakeExec{handler: fakeInject(nil)})
	h := Health()
	checkFix(t, h, "1password token", "yonder secret token set")
	checkFix(t, h, "secret B", "yonder secret add B op://")

	// bws: no cli, then a token present
	setupHome(t, Settings{Provider: ProviderBWS}, nil)
	installFake(t, &fakeExec{handler: fakeInject(nil), missing: map[string]bool{"bws": true}})
	checkFix(t, Health(), "bws cli", "bitwarden.com")
	installFake(t, &fakeExec{handler: func(call) (string, string, error) { return "bws 1.0.0\n", "", nil }})
	installTokens(t, &fakeTokens{tokens: map[string]string{"bws": "0.tok"}})
	for _, c := range Health() {
		if !c.OK {
			t.Fatalf("want all ok: %+v", c)
		}
	}
}

func TestKeychainSetKeepsTokenOffArgv(t *testing.T) {
	f := &fakeExec{handler: func(call) (string, string, error) { return "", "", nil }}
	installFake(t, f)
	if err := (keychain{}).Set("1password", "ops_abc.DEF-123="); err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	if c.bin != securityBin || strings.Join(c.args, " ") != "-i" {
		t.Fatalf("argv: %s %v", c.bin, c.args)
	}
	if c.stdin != "add-generic-password -U -s yonder -a 1password -l yonder -w ops_abc.DEF-123=\n" {
		t.Fatalf("stdin: %q", c.stdin)
	}
	if (keychain{}).Set("1password", "has space") == nil || (keychain{}).Set("1password", "x\nquit") == nil {
		t.Fatal("tokens that could break the command line must be refused")
	}
}

func TestFileStore(t *testing.T) {
	fs := fileStore{dir: filepath.Join(t.TempDir(), "tokens")}
	if _, err := fs.Get("bws"); !errors.Is(err, ErrNoToken) {
		t.Fatal(err)
	}
	if err := fs.Set("bws", "0.tok"); err != nil {
		t.Fatal(err)
	}
	if v, err := fs.Get("bws"); err != nil || v != "0.tok" {
		t.Fatalf("%q %v", v, err)
	}
	if err := os.Chmod(fs.path("bws"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Get("bws"); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("got %v", err)
	}
}

func TestSetToken(t *testing.T) {
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeDesktop}, nil)
	if _, err := SetToken("ops_x"); err == nil || !strings.Contains(err.Error(), "does not use a stored token") {
		t.Fatalf("got %v", err)
	}
	setupHome(t, Settings{Provider: ProviderBWS}, nil)
	ft := &fakeTokens{tokens: map[string]string{}}
	installTokens(t, ft)
	if _, err := SetToken("0.tok"); err != nil || ft.tokens["bws"] != "0.tok" {
		t.Fatalf("%v %v", err, ft.tokens)
	}
}

func TestSetupNonInteractive(t *testing.T) {
	setupHome(t, Settings{}, nil)
	installTokens(t, &fakeTokens{tokens: map[string]string{}})
	var out strings.Builder
	if err := Setup(SetupOptions{Out: &out}); err != nil || !strings.Contains(out.String(), "not set up") {
		t.Fatalf("%v %q", err, out.String())
	}
	out.Reset()
	if err := Setup(SetupOptions{Out: &out, Provider: Provider1Password, Mode: ModeServiceAccount}); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadSettings()
	if s.Provider != Provider1Password || s.Mode != ModeServiceAccount || !strings.Contains(out.String(), "yonder secret token set") {
		t.Fatalf("%+v %q", s, out.String())
	}
	if err := Setup(SetupOptions{Out: &out, Provider: "vaultish"}); err == nil {
		t.Fatal("unknown provider")
	}
}
