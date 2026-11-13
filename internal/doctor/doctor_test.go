package doctor

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/secrets"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

var now = time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)

type fakeRemote struct {
	version map[string]string
	err     map[string]error
	names   map[string][]string
	skills  map[string]agents.Report
	keys    map[string]string
}

func (f fakeRemote) Version(m registry.Machine) (string, error) {
	if err := f.err[m.Name]; err != nil {
		return "", err
	}
	return f.version[m.Name], nil
}

func (f fakeRemote) Registry(m registry.Machine) (registry.Bundle, error) {
	var b registry.Bundle
	for _, n := range f.names[m.Name] {
		b.Machines = append(b.Machines, registry.Machine{Name: n})
	}
	return b, nil
}

func (f fakeRemote) Skills(m registry.Machine) (agents.Report, error) {
	return f.skills[m.Name], nil
}

func (f fakeRemote) AuthorizedKeys(m registry.Machine) (string, error) { return f.keys[m.Name], nil }

func machine(name string, discovered time.Time) registry.Machine {
	return registry.Machine{Name: name, OS: "linux", Arch: "amd64", TailscaleIP: "100.64.0.9", SSHUser: "me", SSHPort: 22,
		SSHHostKey: "ssh-ed25519 AAAAhost", UpdatedAt: discovered, DiscoveredAt: discovered, AddedAt: discovered}
}

func peer(name string, online bool) tailnet.Peer {
	return tailnet.Peer{HostName: name, DNSName: name + ".example.ts.net", TailscaleIPs: []string{"100.64.0.9"}, Online: online, LastSeen: now.Add(-2 * time.Hour), UserID: 1001}
}

func env(t *testing.T, ms ...registry.Machine) Env {
	t.Setenv("PATH", t.TempDir())
	s := &registry.Store{Dir: t.TempDir()}
	if err := s.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	for _, m := range append([]registry.Machine{machine("laptop", now)}, ms...) {
		if err := s.SaveMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	return Env{
		Self: "laptop", Store: s, Fixture: "status.json", Version: "1.0.0",
		Home: t.TempDir(), StateDir: t.TempDir(), Now: now,
		Status: &tailnet.Status{BackendState: "Running", Self: peer("laptop", true)},
	}
}

func find(cs []Check, group, name string) (Check, bool) {
	for _, c := range cs {
		if c.Group == group && c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}

func TestAllGood(t *testing.T) {
	e := env(t, machine("gpu-box", now))
	e.Status.Peers = []tailnet.Peer{peer("gpu-box", true)}
	e.Remote = fakeRemote{version: map[string]string{"gpu-box": "1.0.0"}, names: map[string][]string{"gpu-box": {"gpu-box", "laptop"}}}
	r := NewReport("laptop", "1.0.0", Run(e))
	if !r.OK || r.ExitCode() != 0 || r.Summary.Fail+r.Summary.Warn != 0 {
		t.Fatalf("want all pass: %+v", r)
	}
	var buf bytes.Buffer
	Render(&buf, r, false)
	if !strings.Contains(buf.String(), "All ") || !strings.Contains(buf.String(), "PASS  ") {
		t.Fatalf("render:\n%s", buf.String())
	}
}

func TestAggregation(t *testing.T) {
	old := now.Add(-10 * 24 * time.Hour)
	pending := machine("new-box", time.Time{})
	pending.Pending, pending.SSHHostKey = true, ""
	gpu := machine("gpu-box", old)
	gpu.Facts.Harnesses = []registry.Harness{{Name: "codex"}}
	e := env(t, gpu, machine("desktop", now), machine("sleepy", now), pending)
	e.Status.Peers = []tailnet.Peer{peer("gpu-box", true), peer("desktop", true), peer("sleepy", false), peer("new-box", true)}
	e.Remote = fakeRemote{
		version: map[string]string{"gpu-box": "0.9.0"},
		err:     map[string]error{"desktop": &transport.UnreachableError{Target: "desktop", Detail: "Permission denied (publickey)."}},
		names:   map[string][]string{"gpu-box": {"gpu-box", "laptop"}},
		skills: map[string]agents.Report{"gpu-box": {Harnesses: []agents.HarnessStatus{
			{Name: "codex", Detected: true, State: agents.StateMissing},
			{Name: "hermes", Detected: false, State: agents.StateMissing},
		}}},
	}
	e.Secrets = []secrets.Check{{Name: "provider", OK: false, Detail: "op is not signed in", Fix: "op signin"}}
	cs := Run(e)
	r := NewReport("laptop", "1.0.0", cs)
	if r.OK || r.ExitCode() != 1 {
		t.Fatalf("a FAIL should make exit 1: %+v", r.Summary)
	}
	want := []struct {
		group, name string
		st          Status
		fix         string
	}{
		{"desktop", "ssh", Fail, "ssh-copy-id -p 22 me@100.64.0.9, or if you reach desktop with a specific key: yonder ssh-key desktop <key>"},
		{"sleepy", "online", Warn, ""},
		{"gpu-box", "version", Warn, ""},
		{"gpu-box", "facts", Warn, "yonder refresh gpu-box"},
		{"gpu-box", "skill: codex", Warn, "yonder agents install --all-machines"},
		{"new-box", "setup", Warn, "yonder add new-box"}, // no held pin: only add may trust a key
		{"fleet", "registry", Warn, "yonder refresh"},
		{"secrets", "provider", Fail, "op signin"},
	}
	for _, w := range want {
		c, ok := find(cs, w.group, w.name)
		if !ok || c.Status != w.st || (w.fix != "" && c.Fix != w.fix) {
			t.Errorf("%s/%s: got %+v (found %v), want %s fix %q", w.group, w.name, c, ok, w.st, w.fix)
		}
	}
	if c, _ := find(cs, "new-box", "setup"); !strings.Contains(c.Detail, "new-box is online now; run `yonder add new-box` to finish setup") {
		t.Errorf("pending detail: %q", c.Detail)
	}
	// a pending record holding a pin finishes on refresh, strictly
	held := machine("new-box", time.Time{})
	held.Pending = true
	e = env(t, held)
	e.Status.Peers = []tailnet.Peer{peer("new-box", true)}
	if c, ok := find(Run(e), "new-box", "setup"); !ok || c.Fix != "yonder refresh new-box" {
		t.Errorf("held pin: %+v", c)
	}
	if _, ok := find(cs, "gpu-box", "skill: hermes"); ok {
		t.Error("undetected harnesses shouldn't get a check")
	}
	if c, _ := find(cs, "sleepy", "online"); !strings.Contains(c.Detail, "last seen 2h ago") {
		t.Errorf("offline detail: %q", c.Detail)
	}
	if _, ok := find(cs, "desktop", "version"); ok {
		t.Error("checks after a failed ssh should be skipped")
	}
	if c, _ := find(cs, "fleet", "registry"); !strings.Contains(c.Detail, "gpu-box lacks desktop, new-box, sleepy") {
		t.Errorf("registry detail: %q", c.Detail)
	}
	if r.Summary.Fail != 2 {
		t.Errorf("fail count %d", r.Summary.Fail)
	}
	var buf bytes.Buffer
	Render(&buf, r, false)
	out := buf.String()
	if !strings.Contains(out, "fix: ssh-copy-id") || !strings.Contains(out, "2 failed") || !strings.Contains(out, "Start with the FAIL lines") {
		t.Fatalf("render:\n%s", out)
	}
}

func TestNotSetUpAndTailscaleDown(t *testing.T) {
	e := env(t)
	e.Self = ""
	e.Status, e.StatusErr = nil, errors.New(tailnet.ErrNotRunning.Error()+" (state NeedsLogin)")
	cs := Run(e)
	if c, _ := find(cs, groupLocal, "tailscale logged in"); c.Status != Fail || c.Fix != "tailscale up" {
		t.Fatalf("logged in: %+v", c)
	}
	if c, _ := find(cs, groupLocal, "set up"); c.Status != Fail || c.Fix != "yonder init" {
		t.Fatalf("set up: %+v", c)
	}
	if c, _ := find(cs, "fleet", "fleet"); c.Status != Warn {
		t.Fatalf("fleet should be skipped: %+v", cs)
	}
}

func TestRemovedSelf(t *testing.T) {
	e := env(t)
	if err := e.Store.Apply(registry.Bundle{Tombstones: []registry.Tombstone{{Name: "laptop", RemovedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	c, _ := find(Run(e), groupLocal, "self record")
	if c.Status != Fail || !strings.Contains(c.Detail, "removed from the fleet") {
		t.Fatalf("%+v", c)
	}
}

func TestLostKeyStillTrusted(t *testing.T) {
	e := env(t)
	key := "ssh-ed25519 AAAAlost me@old-box"
	if _, err := e.Store.MergeIn(registry.Bundle{Tombstones: []registry.Tombstone{{Name: "old-box", RemovedAt: now, Lost: true, SSHClientKeys: []string{key}}}}); err != nil {
		t.Fatal(err)
	}
	cs := keyChecks(groupLocal, "restrict "+key+"\n", lostKeys([]registry.Tombstone{{Name: "old-box", Lost: true, SSHClientKeys: []string{key}}}, nil))
	if len(cs) != 1 || cs[0].Status != Fail || cs[0].Fix != "yonder remove old-box --lost --yes" {
		t.Fatalf("%+v", cs)
	}
}

func TestAddressChanged(t *testing.T) {
	m := machine("gpu-box", now)
	m.TailscaleIP = "100.64.0.2"
	e := env(t, m)
	e.Status.Peers = []tailnet.Peer{peer("gpu-box", true)}
	e.Remote = fakeRemote{version: map[string]string{"gpu-box": "1.0.0"}}
	c, ok := find(Run(e), "gpu-box", "address")
	if !ok || c.Status != Fail || !strings.Contains(c.Detail, "record has 100.64.0.2, tailnet has 100.64.0.9") || c.Fix != "yonder remove gpu-box && yonder add gpu-box" {
		t.Fatalf("got %+v", c)
	}
}

func TestRemoteSkillOnlyForFactsHarnesses(t *testing.T) {
	m := machine("gpu-box", now)
	m.Facts.Harnesses = []registry.Harness{{Name: "claude-code"}, {Name: "codex"}, {Name: "pi"}}
	e := env(t, m)
	e.Status.Peers = []tailnet.Peer{peer("gpu-box", true)}
	var hs []agents.HarnessStatus
	for _, n := range []string{"claude-code", "codex", "pi", "hermes"} {
		// the remote says every harness is detected, e.g. a stray ~/.hermes dir
		hs = append(hs, agents.HarnessStatus{Name: n, Detected: true, State: agents.StateMissing})
	}
	e.Remote = fakeRemote{version: map[string]string{"gpu-box": "1.0.0"}, skills: map[string]agents.Report{"gpu-box": {Harnesses: hs}}}
	cs := Run(e)
	for _, n := range []string{"claude-code", "codex", "pi"} {
		if _, ok := find(cs, "gpu-box", "skill: "+n); !ok {
			t.Errorf("missing check for %s", n)
		}
	}
	if _, ok := find(cs, "gpu-box", "skill: hermes"); ok {
		t.Error("hermes isn't in the machine's facts, so no check")
	}
}

func TestMissingIdentityFile(t *testing.T) {
	t.Setenv("YONDER_HOME", t.TempDir())
	e := env(t, machine("gpu-box", now))
	e.Status.Peers = []tailnet.Peer{peer("gpu-box", true)}
	e.Remote = fakeRemote{version: map[string]string{"gpu-box": "1.0.0"}, names: map[string][]string{"gpu-box": {"gpu-box", "laptop"}}}
	if err := e.Store.SetIdentity("gpu-box", "~/.ssh/id_example"); err != nil {
		t.Fatal(err)
	}
	c, ok := find(Run(e), "this machine", "ssh key for gpu-box")
	if !ok || c.Status != Fail || !strings.Contains(c.Detail, "does not exist") || !strings.Contains(c.Fix, "yonder ssh-key gpu-box") {
		t.Fatalf("missing key: %+v (found %v)", c, ok)
	}
	// ~ resolves under the (temp) home, so create it there
	key := registry.ExpandHome("~/.ssh/id_example")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _ := find(Run(e), "this machine", "ssh key for gpu-box"); c.Status != Pass {
		t.Fatalf("existing key: %+v", c)
	}
}

// remove --lost won't rewrite a symlinked authorized_keys, so doctor keeps failing until the lines go
func TestLostKeyThroughSymlinkStillFails(t *testing.T) {
	e := env(t)
	key := "ssh-ed25519 AAAAlost me@old-box"
	if err := e.Store.SaveTombstone(registry.Tombstone{Name: "old-box", RemovedAt: now, Lost: true, SSHClientKeys: []string{key}}); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(t.TempDir(), "dotfiles-authorized_keys")
	if err := os.WriteFile(real, []byte("restrict "+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.Home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(e.Home, ".ssh", "authorized_keys")); err != nil {
		t.Fatal(err)
	}
	c, ok := find(Run(e), groupLocal, "authorized_keys")
	if !ok || c.Status != Fail || !strings.Contains(c.Fix, "by hand") || !strings.Contains(c.Detail, "symlink") {
		t.Fatalf("want a FAIL naming the symlink: %+v", c)
	}
}

func TestMeshUnreachedAndLeftover(t *testing.T) {
	const (
		laptopKey = "ssh-ed25519 AAAAlaptop me@laptop"
		gpuKey    = "ssh-ed25519 AAAAgpu me@gpu-box"
	)
	gpu := machine("gpu-box", now)
	gpu.DNSName, gpu.SSHClientKeys, gpu.MeshKeys = "gpu-box.example.ts.net", []string{gpuKey, "ssh-ed25519 AAAAsandbox x"}, []string{gpuKey}
	e := env(t, gpu)
	self, _ := e.Store.Machine("laptop")
	self.SSHClientKeys, self.MeshKeys = []string{laptopKey}, []string{laptopKey}
	if err := e.Store.SaveMachine(self); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SaveTombstone(registry.Tombstone{Name: "old-box", RemovedAt: now}); err != nil {
		t.Fatal(err)
	}
	e.Status.Peers = []tailnet.Peer{peer("gpu-box", true)}
	e.Remote = fakeRemote{version: map[string]string{"gpu-box": "1.0.0"}, names: map[string][]string{"gpu-box": {"gpu-box", "laptop"}},
		keys: map[string]string{"gpu-box": "ssh-ed25519 AAAAold yonder:old-box\n"}}
	ak := transport.AuthorizedKeysPath(e.Home)
	_ = os.MkdirAll(filepath.Dir(ak), 0o700)
	if err := os.WriteFile(ak, []byte(gpuKey+"\n"+"ssh-ed25519 AAAAsandbox yonder:gpu-box\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cs := Run(e)
	if c, ok := find(cs, groupLocal, "mesh"); !ok || c.Status != Warn || !strings.Contains(c.Detail, "no longer grants, of gpu-box (1)") || c.Fix != "yonder mesh" {
		t.Fatalf("this machine trusts a key gpu-box doesn't use: %+v", c)
	}
	var warns []Check
	for _, c := range cs {
		if c.Group == "gpu-box" && c.Name == "mesh" {
			warns = append(warns, c)
		}
	}
	if len(warns) != 2 || !strings.Contains(warns[0].Detail, "laptop can't ssh in") || warns[0].Fix != "yonder mesh gpu-box" ||
		!strings.Contains(warns[1].Detail, "removed machine old-box") || !strings.HasPrefix(warns[1].Fix, "yonder remove old-box --yes") {
		t.Fatalf("gpu-box mesh checks: %+v", warns)
	}
}

// a config.toml from before v0.3.1 still says model_ports. doctor stays clean
// and says once that it can go.
func TestOldModelPortsNote(t *testing.T) {
	e := env(t, machine("gpu-box", now))
	e.Status.Peers = []tailnet.Peer{peer("gpu-box", true)}
	e.Remote = fakeRemote{version: map[string]string{"gpu-box": "1.0.0"}, names: map[string][]string{"gpu-box": {"gpu-box", "laptop"}}}
	if _, ok := find(Run(e), groupLocal, "config"); ok {
		t.Fatal("no note without model_ports")
	}
	cfg := "self = \"laptop\"\n\n[discover]\nmodel_ports = [9292]\n"
	if err := os.WriteFile(filepath.Join(e.Store.Dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewReport("laptop", "1.0.0", Run(e))
	c, ok := find(r.Checks, groupLocal, "config")
	if !ok || c.Status != Pass || !strings.Contains(c.Detail, "model_ports") || !strings.Contains(c.Detail, "it can go") {
		t.Fatalf("note %+v", c)
	}
	if !r.OK || r.Summary.Warn != 0 {
		t.Fatalf("want clean: %+v", r.Summary)
	}
}
