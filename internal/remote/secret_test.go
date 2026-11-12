package remote

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/secrets"
)

func TestSecretProvision(t *testing.T) {
	setup(t)
	if err := registry.Open().SaveMachine(registry.Machine{Name: "gpu-box"}); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SaveSettings(secrets.Settings{TokenStore: "file"}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	t.Setenv(secrets.ProvisionTokenEnv, "")
	if code := SecretProvision(context.Background(), []string{"yonder-gpu-box", "Agents"}, nil, &out, &errb); code != 1 {
		t.Fatalf("no token: exit %d", code)
	}

	const tok = "ops_DUMMYtoken9"
	t.Setenv(secrets.ProvisionTokenEnv, tok)
	errb.Reset()
	if code := SecretProvision(context.Background(), []string{"yonder-gpu-box", "Agents"}, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "token stored in plain file") || strings.Contains(out.String()+errb.String(), tok) {
		t.Fatalf("output: %q %q", out.String(), errb.String())
	}
	s, _ := secrets.LoadSettings()
	if s.Provider != "1password" || s.Mode != "service-account" || s.ServiceAccount != "yonder-gpu-box" || s.Vault != "Agents" {
		t.Fatalf("settings: %+v", s)
	}
	if b, _ := os.ReadFile(filepath.Join(registry.ConfigDir(), "tokens", "1password")); strings.TrimSpace(string(b)) != tok {
		t.Fatalf("token file: %q", b)
	}
	if m, _ := registry.Open().Machine("gpu-box"); m.SecretsAccount != "yonder-gpu-box" || m.UpdatedAt.IsZero() {
		t.Fatalf("own record: %+v", m)
	}

	out.Reset()
	if code := SecretProbe(context.Background(), []string{"--bogus"}, nil, &out, &errb); code != 2 {
		t.Fatal("unknown flag")
	}
}

func TestSecretProvisionExtraVaults(t *testing.T) {
	setup(t)
	if err := secrets.SaveSettings(secrets.Settings{TokenStore: "file"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(secrets.ProvisionTokenEnv, "ops_DUMMYtoken9")
	var out, errb bytes.Buffer
	args := []string{"yonder-gpu-box", "Agents", "--extra-vault", "Ops", "--write", "--extra-vault", "Build Keys"}
	if code := SecretProvision(context.Background(), args, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s, _ := secrets.LoadSettings()
	if s.Vault != "Agents" || !s.Write || strings.Join(s.ExtraVaults, ",") != "Ops,Build Keys" {
		t.Fatalf("settings: %+v", s)
	}
	if p := secrets.Probe(false); strings.Join(p.ExtraVaults, ",") != "Ops,Build Keys" || !p.CanExtraVaults {
		t.Fatalf("probe: %+v", p)
	}
	for _, bad := range [][]string{
		{"yonder-gpu-box", "Agents", "--extra-vault"},
		{"yonder-gpu-box", "Agents", "--bogus"},
	} {
		if code := SecretProvision(context.Background(), bad, nil, &out, &errb); code != 2 {
			t.Errorf("%v: exit %d", bad, code)
		}
	}
	if code := SecretProvision(context.Background(), []string{"a", "Agents", "--extra-vault", "Personal"}, nil, &out, &errb); code != 1 {
		t.Fatalf("reserved vault: exit %d", code)
	}
}
