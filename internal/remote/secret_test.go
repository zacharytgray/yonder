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
