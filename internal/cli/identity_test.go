package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

// pinSSH fakes Pin: the probe either answers or fails key auth.
type pinSSH struct {
	fakeSSH
	authFail bool
	pins     []transport.Target
}

func (p *pinSSH) Pin(_ context.Context, t transport.Target, _ string, stdio transport.IO) (string, int, error) {
	p.pins = append(p.pins, t)
	if p.authFail {
		return "ssh-ed25519 AAAAhost", 255, &transport.UnreachableError{Target: t.Name, Detail: "me@100.64.0.9: Permission denied (publickey)."}
	}
	_, _ = stdio.Stdout.Write([]byte("yonder=/home/me/.local/bin/yonder\nos=Linux\narch=x86_64\nhome=/home/me\n"))
	return "ssh-ed25519 AAAAhost", 0, nil
}

func newPinApp(t *testing.T, authFail bool) (*app, *pinSSH) {
	t.Helper()
	p := &pinSSH{authFail: authFail}
	p.yonder = func(_ transport.Target, args []string, stdio transport.IO) (int, error) {
		if args[0] == "_facts" {
			_, _ = stdio.Stdout.Write([]byte(`{"hostname":"gpu-box"}`))
		}
		return 0, nil
	}
	a, _ := testApp(t, p, &tailnet.Status{Self: peer("laptop", true), Peers: []tailnet.Peer{peer("gpu-box", true)}})
	if err := a.store.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	return a, p
}

func TestAddIdentityStaysLocal(t *testing.T) {
	a, p := newPinApp(t, false)
	key := filepath.Join(t.TempDir(), "id_example")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pr := peer("gpu-box", true)
	m, err := a.addPeer(pr, addOpts{user: "me", port: 22, identity: key})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.store.Identity("gpu-box"); got != key {
		t.Fatalf("config identity %q, want %q", got, key)
	}
	if len(p.pins) != 1 || p.pins[0].Identity != key {
		t.Fatalf("probe should use the key: %+v", p.pins)
	}
	// the later calls (facts, _init-self) pick it up from config
	if len(p.yonders) == 0 {
		t.Fatal("no yonder calls")
	}
	if tg := a.target(m); tg.Identity != key {
		t.Fatalf("target identity %q", tg.Identity)
	}
	// the record that syncs to peers never holds it
	raw, err := os.ReadFile(filepath.Join(a.store.Dir, "machines", "gpu-box.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "identity") || strings.Contains(string(raw), "id_example") {
		t.Fatalf("record leaks the key path:\n%s", raw)
	}
	// a re-add without the flag keeps using it
	if o := a.withDefaults("gpu-box", addOpts{}); o.identity != key {
		t.Fatalf("re-add identity %q", o.identity)
	}
}

func TestTargetIdentityIsPerMachine(t *testing.T) {
	a, _ := testApp(t, &fakeSSH{}, nil)
	if err := a.store.SetIdentity("gpu-box", "~/.ssh/id_example"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(registry.UserHome(), ".ssh", "id_example")
	if got := a.target(registry.Machine{Name: "gpu-box"}).Identity; got != want {
		t.Fatalf("got %q, want %q (~ expanded at use)", got, want)
	}
	if got := a.target(registry.Machine{Name: "desk"}).Identity; got != "" {
		t.Fatalf("desk got %q", got)
	}
}

func TestAddAuthFailureNamesBothFixes(t *testing.T) {
	a, _ := newPinApp(t, true)
	_, err := a.addPeer(peer("gpu-box", true), addOpts{user: "me", port: 22})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"ssh-copy-id", "yonder add gpu-box --identity <key>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if a.store.Identity("gpu-box") != "" {
		t.Fatal("a failed add must not store a key")
	}
}

func TestSSHKeyCommand(t *testing.T) {
	a, out := testApp(t, &fakeSSH{}, nil)
	if err := a.store.SaveMachine(registry.Machine{Name: "gpu-box"}); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "id_example")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.sshKeyCommand([]string{"gpu-box", key}, false); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.sshKeyCommand([]string{"gpu-box"}, false); err != nil || !strings.Contains(out.String(), key) {
		t.Fatalf("show: %v %q", err, out.String())
	}
	if err := a.sshKeyCommand([]string{"gpu-box", key + "-nope"}, false); err == nil || ExitCode(err) != ExitUsage {
		t.Fatalf("missing file should be refused: %v", err)
	}
	if err := a.sshKeyCommand([]string{"nope", key}, false); err == nil {
		t.Fatal("unknown machine should be refused")
	}
	if err := a.sshKeyCommand([]string{"gpu-box"}, true); err != nil || a.store.Identity("gpu-box") != "" {
		t.Fatalf("clear: %v", err)
	}
}

func TestParseIdentities(t *testing.T) {
	key := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := parseIdentities([]string{"gpu-box=" + key, "desk=" + key})
	if err != nil || got["gpu-box"] != key || got["desk"] != key {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"gpu-box", "=x", "gpu-box=", "Bad Name=" + key, "gpu-box=" + key + "-missing"} {
		if _, err := parseIdentities([]string{bad}); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}
