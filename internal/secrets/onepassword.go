package secrets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	opEnvToken   = "OP_SERVICE_ACCOUNT_TOKEN"
	opInstallFix = "install the 1Password CLI (`op`): https://developer.1password.com/docs/cli/get-started/"
	opDesktopFix = "install the 1Password app on this machine, then turn on Settings > Developer > \"Integrate with 1Password CLI\""
)

// onePassword runs `op`. desktop mode leans on the app's touch id prompt;
// service-account mode passes a stored token in the child's env.
type onePassword struct {
	mode    string
	tokens  TokenStore
	account string // service account name, when provisioning recorded it
}

func (o *onePassword) Name() string { return Provider1Password }

func (o *onePassword) env() ([]string, error) {
	if o.mode != ModeServiceAccount {
		return childEnv(), nil
	}
	tok, src, err := tokenFor(o.tokens, Provider1Password, opEnvToken)
	if errors.Is(err, ErrNoToken) {
		return nil, fmt.Errorf("no 1password service account token in %s; run `yonder secret token set`", src)
	}
	if err != nil {
		return nil, err
	}
	return childEnv(opEnvToken + "=" + tok), nil
}

// Resolve makes one `op inject` call for every ref: one approval, one request.
func (o *onePassword) Resolve(refs map[string]string) (map[string]string, error) {
	if len(refs) == 0 {
		return map[string]string{}, nil
	}
	for n, r := range refs {
		if err := validOpRef(r); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
	}
	bin, err := lookPath("op")
	if err != nil {
		return nil, errors.New("1password CLI `op` not found; " + opInstallFix)
	}
	env, err := o.env()
	if err != nil {
		return nil, err
	}
	tmpl, boundary := injectTemplate(refs)
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	out, _, err := run(ctx, bin, []string{"inject"}, env, tmpl)
	if err != nil {
		// op's own message is never shown: it can quote the token or a value
		hint := "check that each reference names an item and field that exist and that this machine can read their vault"
		if o.mode == ModeDesktop {
			hint = "is the 1Password app running, unlocked, with CLI integration on? " + hint
		}
		return nil, fmt.Errorf("1Password could not resolve the secrets (op inject exit %d); %s", exitCode(err), hint)
	}
	return parseInject(out, boundary, refs)
}

// injectTemplate builds one block per name between random boundary lines, so a
// value may hold newlines and still parse back unambiguously.
func injectTemplate(refs map[string]string) ([]byte, string) {
	var nonce [12]byte
	_, _ = rand.Read(nonce[:])
	boundary := "--yonder-" + hex.EncodeToString(nonce[:]) + "--"
	var b strings.Builder
	for _, n := range sortedKeys(refs) {
		fmt.Fprintf(&b, "%s %s\n{{ %s }}\n", boundary, n, refs[n])
	}
	fmt.Fprintf(&b, "%s end\n", boundary)
	return []byte(b.String()), boundary
}

// parseInject reverses injectTemplate. its errors never quote the output.
func parseInject(out []byte, boundary string, refs map[string]string) (map[string]string, error) {
	bad := errors.New("op inject returned output yonder could not parse (values not shown)")
	parts := strings.Split(string(out), boundary+" ")
	if len(parts) != len(refs)+2 || parts[0] != "" || strings.TrimRight(parts[len(parts)-1], "\n") != "end" {
		return nil, bad
	}
	vals := map[string]string{}
	for _, p := range parts[1 : len(parts)-1] {
		name, val, ok := strings.Cut(p, "\n")
		if !ok || !strings.HasSuffix(val, "\n") {
			return nil, bad
		}
		if _, want := refs[name]; !want {
			return nil, bad
		}
		if _, dup := vals[name]; dup {
			return nil, bad
		}
		vals[name] = strings.TrimSuffix(val, "\n")
	}
	return vals, nil
}

func (o *onePassword) Health() []Check {
	bin, err := lookPath("op")
	if err != nil {
		return []Check{{Name: "1password cli", Detail: "`op` is not on PATH", Fix: opInstallFix}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	ver, _, err := run(ctx, bin, []string{"--version"}, childEnv(), nil)
	if err != nil {
		return []Check{{Name: "1password cli", Detail: bin + " does not run: " + err.Error(), Fix: opInstallFix}}
	}
	out := []Check{{Name: "1password cli", OK: true, Detail: "op " + strings.TrimSpace(string(ver)) + " at " + bin}}
	if o.mode == ModeServiceAccount {
		return append(out, o.serviceAccountHealth(ctx, bin)...)
	}
	return append(out, o.desktopHealth(ctx, bin))
}

// desktopHealth never reads an item: whoami, then the account list the app exposes.
func (o *onePassword) desktopHealth(ctx context.Context, bin string) Check {
	c := Check{Name: "1password app integration"}
	if _, _, err := run(ctx, bin, []string{"whoami"}, childEnv(), nil); err == nil {
		c.OK, c.Detail = true, "authorized in this session"
		return c
	}
	accts, _, err := run(ctx, bin, []string{"account", "list", "--format", "json"}, childEnv(), nil)
	var list []json.RawMessage
	if err == nil && json.Unmarshal(accts, &list) == nil && len(list) > 0 {
		c.OK = true
		c.Detail = fmt.Sprintf("on (%d account(s)); the first read asks for touch id or system auth, then lasts until 10 min idle or 12 h", len(list))
		return c
	}
	c.Detail = "op cannot see the 1Password app"
	c.Fix = opDesktopFix
	return c
}

func (o *onePassword) serviceAccountHealth(ctx context.Context, bin string) []Check {
	tok, src, err := tokenFor(o.tokens, Provider1Password, opEnvToken)
	if err != nil {
		detail := "no service account token in " + src
		if !errors.Is(err, ErrNoToken) {
			detail = err.Error()
		}
		return []Check{{Name: "1password token", Detail: detail, Fix: "yonder secret token set (paste the service account token)"}}
	}
	detail := "stored in " + src
	if o.account != "" {
		detail += ", service account " + o.account
	}
	out := []Check{{Name: "1password token", OK: true, Detail: detail}}
	_, stderr, err := run(ctx, bin, []string{"whoami"}, childEnv(opEnvToken+"="+tok), nil)
	c := Check{Name: "1password service account"}
	if err != nil {
		c.Detail = "op rejected the token: " + scrubbedLine(stderr, err, map[string]string{"TOKEN": tok})
		c.Fix = "create a new service account token in 1Password, then `yonder secret token set`"
		if o.account != "" {
			c.Fix = fmt.Sprintf("revoke %s at %s, then from a machine with the 1Password app: yonder secret setup --machines %s --replace", o.account, RevokeWhere, selfOrPlaceholder())
		}
	} else {
		c.OK, c.Detail = true, "token accepted"
	}
	return append(out, c)
}
