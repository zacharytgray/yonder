package secrets

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const dummyToken = "ops_dummyTOKEN.abc123"

func TestCheckVaultName(t *testing.T) {
	for _, bad := range []string{"", "Personal", "private", "Shared", "Employee", "a:b", "a,b", "x\ny"} {
		if CheckVaultName(bad) == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	if err := CheckVaultName(DefaultVault); err != nil {
		t.Fatal(err)
	}
	if CheckExpiresIn("30d") != nil || CheckExpiresIn("1w2d") != nil || CheckExpiresIn("30 days") == nil {
		t.Fatal("expires-in check")
	}
}

func TestEnsureVault(t *testing.T) {
	setupHome(t, Settings{}, nil)
	vaults := `[{"id":"1","name":"Other"}]`
	f := &fakeExec{handler: func(c call) (string, string, error) {
		switch strings.Join(c.args[:2], " ") {
		case "vault list":
			return vaults, "", nil
		case "vault create":
			return "", "", nil
		}
		return "", "", errors.New("unexpected")
	}}
	installFake(t, f)
	created, err := EnsureVault(DefaultVault)
	if err != nil || !created {
		t.Fatalf("%v %v", created, err)
	}
	if got := f.calls[1].args; got[0] != "vault" || got[1] != "create" || got[2] != DefaultVault {
		t.Fatalf("create argv: %v", got)
	}
	f.calls = nil
	vaults = `[{"id":"1","name":"Yonder Agents"}]`
	if created, err := EnsureVault(DefaultVault); err != nil || created || len(f.calls) != 1 {
		t.Fatalf("existing vault: %v %v %v", created, err, f.calls)
	}
}

func TestCreateServiceAccount(t *testing.T) {
	setupHome(t, Settings{}, nil)
	t.Setenv(opEnvToken, "ops_someOtherToken") // a user's own token must not leak into op
	f := &fakeExec{handler: func(c call) (string, string, error) { return dummyToken + "\n", "", nil }}
	installFake(t, f)
	tok, err := CreateServiceAccount("yonder-gpu-box", DefaultVault, "30d", false)
	if err != nil || tok != dummyToken {
		t.Fatalf("%q %v", tok, err)
	}
	want := []string{"service-account", "create", "yonder-gpu-box", "--vault", "Yonder Agents:read_items", "--raw", "--expires-in", "30d"}
	if !slices.Equal(f.calls[0].args, want) {
		t.Fatalf("argv %v", f.calls[0].args)
	}
	for _, kv := range f.calls[0].env {
		if strings.HasPrefix(kv, opEnvToken+"=") {
			t.Fatal("op must run as the person, not a service account")
		}
	}

	// junk instead of a token is refused and not echoed
	installFake(t, &fakeExec{handler: func(call) (string, string, error) { return "not a token\n", "", nil }})
	if _, err := CreateServiceAccount("x", "v", "", false); err == nil || strings.Contains(err.Error(), "not a token") {
		t.Fatalf("got %v", err)
	}
}

func TestInstallAndProbe(t *testing.T) {
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeDesktop}, nil)
	ft := &fakeTokens{tokens: map[string]string{}}
	installTokens(t, ft)
	f := &fakeExec{handler: func(c call) (string, string, error) {
		switch c.args[0] {
		case "--version":
			return "2.34.0\n", "", nil
		case "user":
			if slices.Contains(c.env, opEnvToken+"="+dummyToken) {
				return `{"type":"SERVICE_ACCOUNT"}`, "", nil
			}
		}
		return "", "boom", errors.New("exit 1")
	}}
	installFake(t, f)

	if p := Probe(false); p.Op == "" || p.HasToken || p.Mode != ModeDesktop {
		t.Fatalf("before: %+v", p)
	}
	if _, err := InstallServiceAccount(dummyToken, "yonder-gpu-box", DefaultVault, false); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadSettings()
	if s.Mode != ModeServiceAccount || s.ServiceAccount != "yonder-gpu-box" || s.Vault != DefaultVault || ft.tokens[Provider1Password] != dummyToken {
		t.Fatalf("%+v %v", s, ft.tokens)
	}
	p := Probe(true)
	if !p.Verified || !p.HasToken || p.ServiceAccount != "yonder-gpu-box" {
		t.Fatalf("after: %+v", p)
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.args, " "), dummyToken) {
			t.Fatalf("token in argv: %v", c.args)
		}
	}
	// doctor names the account when op rejects the token
	ft.tokens[Provider1Password] = "ops_revoked"
	checkFix(t, Health(), "1password service account", "revoke yonder-gpu-box")
}
