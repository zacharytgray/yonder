package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	bwsEnvToken   = "BWS_ACCESS_TOKEN"
	bwsInstallFix = "install the Bitwarden Secrets Manager CLI (`bws`): https://bitwarden.com/help/secrets-manager-cli/"
)

// bws runs Bitwarden Secrets Manager's cli with a machine-account token.
type bws struct {
	tokens TokenStore
}

func (b *bws) Name() string { return ProviderBWS }

// Resolve runs `bws secret get` once per ref; bws has no batch get by id.
func (b *bws) Resolve(refs map[string]string) (map[string]string, error) {
	if len(refs) == 0 {
		return map[string]string{}, nil
	}
	for n, r := range refs {
		if !bwsRefRe.MatchString(r) {
			return nil, fmt.Errorf("%s: bad bws reference %q (want bws://<secret uuid>)", n, r)
		}
	}
	bin, err := lookPath("bws")
	if err != nil {
		return nil, errors.New("`bws` not found; " + bwsInstallFix)
	}
	tok, src, err := tokenFor(b.tokens, ProviderBWS, bwsEnvToken)
	if errors.Is(err, ErrNoToken) {
		return nil, fmt.Errorf("no bws access token in %s; run `yonder secret token set`", src)
	}
	if err != nil {
		return nil, err
	}
	env := childEnv(bwsEnvToken+"="+tok, "NO_COLOR=1")
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	vals := map[string]string{}
	for _, n := range sortedKeys(refs) {
		id := strings.TrimPrefix(refs[n], "bws://")
		out, _, err := run(ctx, bin, []string{"secret", "get", id, "--output", "json"}, env, nil)
		if err != nil {
			// bws's own message is never shown: it can quote the token or the value
			return nil, fmt.Errorf("%s: Bitwarden could not read the secret (bws exit %d); check the bws:// id and that the access token can read its project", n, exitCode(err))
		}
		v, err := parseBWS(out)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		vals[n] = v
	}
	return vals, nil
}

// parseBWS reads `bws secret get --output json`. its errors never quote the output.
func parseBWS(out []byte) (string, error) {
	var s struct {
		Key   string  `json:"key"`
		Value *string `json:"value"`
	}
	if err := json.Unmarshal(out, &s); err != nil || s.Value == nil {
		return "", errors.New("bws returned output yonder could not parse (values not shown)")
	}
	return *s.Value, nil
}

func (b *bws) Health() []Check {
	bin, err := lookPath("bws")
	if err != nil {
		return []Check{{Name: "bws cli", Detail: "`bws` is not on PATH", Fix: bwsInstallFix}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	ver, _, err := run(ctx, bin, []string{"--version"}, childEnv(), nil)
	if err != nil {
		return []Check{{Name: "bws cli", Detail: bin + " does not run: " + err.Error(), Fix: bwsInstallFix}}
	}
	out := []Check{{Name: "bws cli", OK: true, Detail: strings.TrimSpace(string(ver)) + " at " + bin}}
	_, src, err := tokenFor(b.tokens, ProviderBWS, bwsEnvToken)
	switch {
	case errors.Is(err, ErrNoToken):
		out = append(out, Check{Name: "bws token", Detail: "no machine-account access token in " + src, Fix: "yonder secret token set (paste the machine account access token)"})
	case err != nil:
		out = append(out, Check{Name: "bws token", Detail: err.Error(), Fix: "yonder secret token set"})
	default:
		out = append(out, Check{Name: "bws token", OK: true, Detail: "stored in " + src})
	}
	return out
}
