package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

func sharedStatus(t *testing.T) *tailnet.Status {
	t.Helper()
	data, err := os.ReadFile("../tailnet/testdata/shared.json")
	if err != nil {
		t.Fatal(err)
	}
	st, err := tailnet.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// a foreign same-named node must never be picked by short name, and another
// account's node needs --allow-shared
func TestAddRefusesAmbiguousAndShared(t *testing.T) {
	a, p := newPinApp(t, false)
	a.st = sharedStatus(t)

	err := a.addCommand("gpu-box", addOpts{})
	if err == nil || !strings.Contains(err.Error(), "gpu-box.example.ts.net") || !strings.Contains(err.Error(), "gpu-box.other.example.ts.net") || !strings.Contains(err.Error(), "full name") {
		t.Fatalf("ambiguous name: %v", err)
	}
	for _, name := range []string{"nas", "gpu-box.other.example.ts.net", "friend-box"} {
		if err := a.addCommand(name, addOpts{}); err == nil || !strings.Contains(err.Error(), "--allow-shared") {
			t.Fatalf("%s: want a refusal naming --allow-shared, got %v", name, err)
		}
	}
	if len(p.pins) != 0 {
		t.Fatalf("refused peers were contacted: %+v", p.pins)
	}
	// the full name picks your own; it is offline here, so it's saved pending at its address
	if err := a.addCommand("gpu-box.example.ts.net", addOpts{}); ExitCode(err) != ExitTempErr {
		t.Fatalf("own gpu-box by full name: %v", err)
	}
	if m, err := a.store.Machine("gpu-box"); err != nil || m.TailscaleIP != "100.64.0.2" || m.DNSName != "gpu-box.example.ts.net" {
		t.Fatalf("record %+v %v", m, err)
	}
	// with gpu-box in the fleet, the foreign one can't take its name, even with --allow-shared
	if err := a.addCommand("gpu-box.other.example.ts.net", addOpts{allowShared: true}); err == nil || !strings.Contains(err.Error(), "yonder remove gpu-box") {
		t.Fatalf("foreign node replacing a record: %v", err)
	}
	// --allow-shared without a terminal goes ahead
	if err := a.addCommand("nas", addOpts{allowShared: true}); err != nil {
		t.Fatalf("nas with --allow-shared: %v", err)
	}
	if len(p.pins) != 1 || p.pins[0].Host != "100.64.0.67" {
		t.Fatalf("pins %+v", p.pins)
	}
}

func TestInitMarksSharedPeers(t *testing.T) {
	a, out := testApp(t, &fakeSSH{}, nil)
	st := sharedStatus(t)
	cands := a.initCandidates(st, "laptop", false)
	if _, err := a.pickPeers(st, cands, nil, false, false); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"nas: not owned by your tailscale account", "gpu-box.other.example.ts.net: not owned", "peers you can add: desktop-pc, gpu-box.example.ts.net, old-server\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if _, err := a.pickPeers(st, cands, []string{"gpu-box"}, false, false); err == nil || !strings.Contains(err.Error(), "full name") {
		t.Fatalf("--machines gpu-box should be ambiguous: %v", err)
	}
	if _, err := a.pickPeers(st, cands, []string{"nas"}, false, false); err == nil || !strings.Contains(err.Error(), "--allow-shared") {
		t.Fatalf("--machines nas needs --allow-shared: %v", err)
	}
	out.Reset()
	cands = a.initCandidates(st, "laptop", true)
	if _, err := a.pickPeers(st, cands, nil, false, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nas (not yours)") {
		t.Fatalf("with --allow-shared, shared peers are listed marked:\n%s", out.String())
	}
}

// strictSSH records which targets were probed under a held pin (Shell) and which pinned anew (Pin)
type strictSSH struct {
	pinSSH
	newKey  string
	shellTo []transport.Target
}

func (s *strictSSH) Shell(_ context.Context, t transport.Target, _ string, stdio transport.IO) (int, error) {
	s.shellTo = append(s.shellTo, t)
	_, _ = stdio.Stdout.Write([]byte("yonder=/home/me/.local/bin/yonder\nos=Linux\narch=x86_64\nhome=/home/me\n"))
	return 0, nil
}

func (s *strictSSH) Pin(_ context.Context, t transport.Target, _ string, stdio transport.IO) (string, int, error) {
	s.pins = append(s.pins, t)
	_, _ = stdio.Stdout.Write([]byte("yonder=/home/me/.local/bin/yonder\nos=Linux\narch=x86_64\nhome=/home/me\n"))
	return s.newKey, 0, nil
}

func newStrictApp(t *testing.T, online bool) (*app, *strictSSH) {
	t.Helper()
	s := &strictSSH{newKey: "ssh-ed25519 AAAAnewkey"}
	s.yonder = func(_ transport.Target, args []string, stdio transport.IO) (int, error) {
		switch args[0] {
		case "_facts":
			fmt.Fprint(stdio.Stdout, `{"hostname":"gpu-box"}`)
		case "_sync":
			fmt.Fprint(stdio.Stdout, `{"machines":[],"tombstones":[]}`)
		}
		return 0, nil
	}
	a, _ := testApp(t, s, &tailnet.Status{Self: peer("laptop", true), Peers: []tailnet.Peer{peer("gpu-box", online)}})
	if err := a.store.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if err := a.store.SaveMachine(registry.Machine{Name: "gpu-box", TailscaleIP: "100.64.0.9", DNSName: "gpu-box.example.ts.net",
		SSHUser: "me", SSHPort: 22, SSHHostKey: "ssh-ed25519 AAAAoldkey", YonderPath: "/home/me/.local/bin/yonder",
		SSHClientKeys: []string{}, UpdatedAt: old, AddedAt: old}); err != nil {
		t.Fatal(err)
	}
	return a, s
}

// `add` while a set-up machine sleeps keeps its pin; refresh then finishes
// against that pin and never trusts a new key
func TestPendingKeepsHeldPin(t *testing.T) {
	a, s := newStrictApp(t, false)
	if err := a.addCommand("gpu-box", addOpts{}); ExitCode(err) != ExitTempErr {
		t.Fatalf("offline add: %v", err)
	}
	m, _ := a.store.Machine("gpu-box")
	if !m.Pending || m.SSHHostKey != "ssh-ed25519 AAAAoldkey" {
		t.Fatalf("pending record should hold the old pin: %+v", m)
	}
	a.st.Peers[0].Online = true
	if err := a.refreshOne(m, "laptop"); err != nil {
		t.Fatal(err)
	}
	if len(s.pins) != 0 {
		t.Fatalf("refresh pinned a new key: %+v", s.pins)
	}
	if len(s.shellTo) != 1 || s.shellTo[0].HostKey != "ssh-ed25519 AAAAoldkey" {
		t.Fatalf("probe should run strictly under the held pin: %+v", s.shellTo)
	}
	if m, _ := a.store.Machine("gpu-box"); m.Pending || m.SSHHostKey != "ssh-ed25519 AAAAoldkey" {
		t.Fatalf("finished record %+v", m)
	}
}

// an explicit add re-pins, and says the key changed
func TestAddWarnsOnChangedHostKey(t *testing.T) {
	a, _ := newStrictApp(t, true)
	if err := a.addCommand("gpu-box", addOpts{}); err != nil {
		t.Fatal(err)
	}
	out := a.out.(interface{ String() string }).String()
	if !strings.Contains(out, "ssh host key changed") || !strings.Contains(out, "yonder remove gpu-box --lost") {
		t.Fatalf("no warning about the changed key:\n%s", out)
	}
	if m, _ := a.store.Machine("gpu-box"); m.SSHHostKey != "ssh-ed25519 AAAAnewkey" {
		t.Fatalf("record %+v", m)
	}
}

// sync, log and remove's online check skip a peer whose address moved, before any ssh
func TestMovedPeerGetsNoSSH(t *testing.T) {
	fake := &fakeSSH{yonder: func(transport.Target, []string, transport.IO) (int, error) {
		return 1, fmt.Errorf("should not be called")
	}}
	moved := peer("desk", true)
	moved.TailscaleIPs = []string{"100.64.0.3"}
	a, out := testApp(t, fake, &tailnet.Status{Self: peer("laptop", true), Peers: []tailnet.Peer{moved, peer("new-box", true)}})
	if err := a.store.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	for _, m := range []registry.Machine{
		{Name: "desk", TailscaleIP: "100.64.0.2", SSHHostKey: "ssh-ed25519 AAAAhost", UpdatedAt: now, AddedAt: now},
		{Name: "new-box", TailscaleIP: "100.64.0.9", Pending: true, UpdatedAt: now, AddedAt: now},
	} {
		if err := a.store.SaveMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	desk, _ := a.store.Machine("desk")
	if a.isOnline(desk) {
		t.Fatal("a moved peer is not online for remove")
	}
	if err := a.removeCommand("new-box", false, true, false); err != nil {
		t.Fatal(err)
	}
	if len(fake.yonders) != 0 {
		t.Fatalf("ssh went to a moved peer: %v", fake.yonders)
	}
	if !strings.Contains(out.String(), "desk's address changed") {
		t.Fatalf("sync should say why it skipped desk:\n%s", out.String())
	}
	if _, err := a.reach(desk, "laptop"); ExitCode(err) != ExitError {
		t.Fatalf("reach: %v", err)
	}
}
