package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/secrets"
)

// SecretProbe is `yonder _secret-probe [--verify]`: op, provider settings and
// token presence here as JSON. never a value.
func SecretProbe(_ context.Context, args []string, _ io.Reader, out, errw io.Writer) int {
	verify := false
	for _, a := range args {
		if a != "--verify" {
			fmt.Fprintf(errw, "yonder _secret-probe: unknown argument %s\n", a)
			return 2
		}
		verify = true
	}
	if err := json.NewEncoder(out).Encode(secrets.Probe(verify)); err != nil {
		return 1
	}
	return 0
}

// SecretProvision is `yonder _secret-provision <account> <vault>`. the token
// comes in $YONDER_OP_TOKEN, which _exec sets from the envelope, so it is
// never in argv and _exec scrubs it from output and the log.
func SecretProvision(_ context.Context, args []string, _ io.Reader, out, errw io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(errw, "usage: yonder _secret-provision <account> <vault>  (token in $"+secrets.ProvisionTokenEnv+")")
		return 2
	}
	tok := os.Getenv(secrets.ProvisionTokenEnv)
	if tok == "" {
		fmt.Fprintf(errw, "yonder _secret-provision: no token in $%s; run `yonder secret setup` from a machine with the 1Password app\n", secrets.ProvisionTokenEnv)
		return 1
	}
	kind, err := ProvisionHere(tok, args[0], args[1])
	if err != nil {
		fmt.Fprintf(errw, "yonder _secret-provision: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "token stored in %s\n", kind)
	return 0
}

// ProvisionHere installs a service account token on this machine and notes
// the account name on its own record, so the name syncs to the fleet.
func ProvisionHere(token, account, vault string) (string, error) {
	kind, err := secrets.InstallServiceAccount(token, account, vault)
	if err != nil {
		return "", err
	}
	s := registry.Open()
	if c, err := s.Config(); err == nil && c.Self != "" {
		if m, err := s.Machine(c.Self); err == nil {
			m.SecretsAccount, m.UpdatedAt = account, time.Now().UTC()
			if err := s.SaveMachine(m); err != nil {
				return kind, err
			}
		}
	}
	return kind, nil
}
