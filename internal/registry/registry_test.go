package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)

func rec(name string, at time.Time, notes string) Machine {
	return Machine{Name: name, UpdatedAt: at, AddedAt: at, Notes: notes}
}

func TestMergeNewestWins(t *testing.T) {
	a := Bundle{Machines: []Machine{rec("gpu-box", t0, "old"), rec("laptop", t0, "a")}}
	b := Bundle{Machines: []Machine{rec("gpu-box", t0.Add(time.Hour), "new")}}
	for _, got := range []Bundle{Merge(a, b), Merge(b, a)} {
		if len(got.Machines) != 2 {
			t.Fatalf("want 2 machines, got %d", len(got.Machines))
		}
		if got.Machines[0].Name != "gpu-box" || got.Machines[0].Notes != "new" {
			t.Fatalf("newest record should win: %+v", got.Machines[0])
		}
	}
}

func TestMergeTombstone(t *testing.T) {
	live := Bundle{Machines: []Machine{rec("gpu-box", t0, "")}}
	newer := Bundle{Tombstones: []Tombstone{{Name: "gpu-box", RemovedAt: t0.Add(time.Minute)}}}
	got := Merge(live, newer)
	if len(got.Machines) != 0 || len(got.Tombstones) != 1 {
		t.Fatalf("newer tombstone should drop the record: %+v", got)
	}

	readded := Bundle{Machines: []Machine{rec("gpu-box", t0.Add(time.Hour), "back")}}
	got = Merge(got, readded)
	if len(got.Machines) != 1 || len(got.Tombstones) != 0 {
		t.Fatalf("record newer than tombstone should win and clear it: %+v", got)
	}

	// equal times: the record stays
	got = Merge(Bundle{Machines: []Machine{rec("x", t0, "")}, Tombstones: []Tombstone{{Name: "x", RemovedAt: t0}}})
	if len(got.Machines) != 1 {
		t.Fatalf("tie should keep the record: %+v", got)
	}
}

func TestTombstoneBeatsSelfRefresh(t *testing.T) {
	tomb := Tombstone{Name: "gpu-box", RemovedAt: t0.Add(time.Hour), Lost: true}
	// the removed machine came back and refreshed itself: newer updated_at, old added_at
	ghost := Machine{Name: "gpu-box", AddedAt: t0, UpdatedAt: t0.Add(2 * time.Hour)}
	for _, got := range []Bundle{
		Merge(Bundle{Tombstones: []Tombstone{tomb}}, Bundle{Machines: []Machine{ghost}}),
		Merge(Bundle{Machines: []Machine{ghost}}, Bundle{Tombstones: []Tombstone{tomb}}),
	} {
		if len(got.Machines) != 0 || len(got.Tombstones) != 1 || !got.Tombstones[0].Lost {
			t.Fatalf("tombstone should beat a self-refreshed record: %+v", got)
		}
	}
	// a re-add elsewhere can't beat a lost tombstone; only a local add clears it
	readd := Machine{Name: "gpu-box", AddedAt: t0.Add(90 * time.Minute), UpdatedAt: t0.Add(90 * time.Minute)}
	got := Merge(Bundle{Tombstones: []Tombstone{tomb}}, Bundle{Machines: []Machine{readd}}, Bundle{Machines: []Machine{ghost}})
	if len(got.Machines) != 0 || len(got.Tombstones) != 1 {
		t.Fatalf("lost tombstone should win: %+v", got)
	}
	// a plain tombstone still loses to a re-add, which keeps its added_at even if the ghost's record is newer
	plain := tomb
	plain.Lost = false
	got = Merge(Bundle{Tombstones: []Tombstone{plain}}, Bundle{Machines: []Machine{readd}}, Bundle{Machines: []Machine{ghost}})
	if len(got.Machines) != 1 || len(got.Tombstones) != 0 || !got.Machines[0].AddedAt.Equal(readd.AddedAt) {
		t.Fatalf("re-add should win: %+v", got)
	}
}

func TestTombstoneTiePrefersLost(t *testing.T) {
	plain := Tombstone{Name: "x", RemovedAt: t0}
	lost := Tombstone{Name: "x", RemovedAt: t0, Lost: true, SSHClientKeys: []string{"ssh-ed25519 AAAAk"}}
	for _, got := range []Bundle{Merge(Bundle{Tombstones: []Tombstone{plain}}, Bundle{Tombstones: []Tombstone{lost}}),
		Merge(Bundle{Tombstones: []Tombstone{lost}}, Bundle{Tombstones: []Tombstone{plain}})} {
		if !got.Tombstones[0].Lost {
			t.Fatalf("lost tombstone should win a tie: %+v", got)
		}
	}
}

func TestForget(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(s.Dir, "config.toml"), []byte("self = \"gpu-box\"\n[secrets]\nprovider = \"bws\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMachine(rec("gpu-box", t0, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTombstone(Tombstone{Name: "old", RemovedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(); err != nil {
		t.Fatal(err)
	}
	b, err := s.Bundle()
	if err != nil || len(b.Machines)+len(b.Tombstones) != 0 {
		t.Fatalf("registry should be empty: %+v %v", b, err)
	}
	c, _ := s.Config()
	if c.Self != "" || c.Secrets["provider"] != "bws" {
		t.Fatalf("forget should clear self and keep the rest: %+v", c)
	}
}

func TestMergeEmptyIsStable(t *testing.T) {
	got := Merge()
	if got.Machines == nil || got.Tombstones == nil {
		t.Fatal("empty merge should give empty slices, not nil")
	}
}

func TestDigest(t *testing.T) {
	a := Bundle{Machines: []Machine{rec("a", t0, ""), rec("b", t0, "")}}
	b := Bundle{Machines: []Machine{rec("b", t0, "x"), rec("a", t0, "y")}}
	if a.Digest() != b.Digest() {
		t.Fatal("digest should depend on names and times only")
	}
	c := Bundle{Machines: []Machine{rec("a", t0.Add(time.Second), ""), rec("b", t0, "")}}
	if a.Digest() == c.Digest() {
		t.Fatal("digest should change with updated_at")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	m := Machine{
		Name: "gpu-box", OS: "linux", Arch: "amd64", TailscaleIP: "100.64.0.2",
		DNSName: "gpu-box.example.ts.net", SSHUser: "me", SSHPort: 22,
		YonderPath: "/home/me/.local/bin/yonder", SSHHostKey: "ssh-ed25519 AAAAexample",
		SSHClientKeys: []string{"ssh-ed25519 AAAAclient me@gpu-box"},
		Role:          "gpu", Notes: "sleeps at night", UpdatedAt: t0, DiscoveredAt: t0,
		Facts: Facts{
			CPU: "Example CPU", CPUCores: 16, MemoryGB: 64,
			GPUs:      []GPU{{Name: "Example GPU", Vendor: "nvidia", MemoryGB: 24}},
			Models:    []Model{{Name: "example:7b", Runtime: "ollama", SizeGB: 4.1}},
			Harnesses: []Harness{{Name: "claude-code", Version: "2.0.0"}},
		},
	}
	if err := s.SaveMachine(m); err != nil {
		t.Fatal(err)
	}
	got, err := s.Machine("gpu-box")
	if err != nil {
		t.Fatal(err)
	}
	if got.Facts.GPUs[0].Name != "Example GPU" || got.Facts.Models[0].Runtime != "ollama" || !got.UpdatedAt.Equal(t0) {
		t.Fatalf("round trip lost data: %+v", got)
	}
	data, _ := os.ReadFile(filepath.Join(s.Dir, "machines", "gpu-box.toml"))
	if !strings.Contains(string(data), `name = 'gpu-box'`) && !strings.Contains(string(data), `name = "gpu-box"`) {
		t.Fatalf("unexpected toml:\n%s", data)
	}
	if _, err := s.Machine("nope"); err == nil {
		t.Fatal("want not found")
	}
	if err := s.SaveMachine(Machine{Name: "../evil"}); err == nil {
		t.Fatal("bad names must be refused")
	}
}

func TestApplyPrunes(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	for _, n := range []string{"a", "b"} {
		if err := s.SaveMachine(rec(n, t0, "")); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := s.MergeIn(Bundle{Tombstones: []Tombstone{{Name: "b", RemovedAt: t0.Add(time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Machines) != 1 || merged.Machines[0].Name != "a" {
		t.Fatalf("got %+v", merged)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "machines", "b.toml")); !os.IsNotExist(err) {
		t.Fatal("b.toml should be gone")
	}
	if _, ok := s.Tombstone("b"); !ok {
		t.Fatal("tombstone should be on disk")
	}
}

func TestSetSelfKeepsOtherKeys(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(s.Dir, "config.toml"), []byte("self = \"old\"\n[secrets]\nprovider = \"bws\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	c, err := s.Config()
	if err != nil {
		t.Fatal(err)
	}
	if c.Self != "laptop" || c.Secrets["provider"] != "bws" {
		t.Fatalf("got %+v", c)
	}
}

func TestPaths(t *testing.T) {
	t.Setenv("YONDER_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "/x/config")
	t.Setenv("XDG_STATE_HOME", "/x/state")
	if ConfigDir() != "/x/config/yonder" || StateDir() != "/x/state/yonder" {
		t.Fatalf("xdg: %s %s", ConfigDir(), StateDir())
	}
	t.Setenv("YONDER_HOME", "/tmp/ch")
	if ConfigDir() != "/tmp/ch/config" || StateDir() != "/tmp/ch/state" || LogPath() != "/tmp/ch/state/log.jsonl" {
		t.Fatalf("yonder home: %s %s", ConfigDir(), StateDir())
	}
	t.Setenv("YONDER_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/h/me")
	if ConfigDir() != "/h/me/.config/yonder" || StateDir() != "/h/me/.local/state/yonder" {
		t.Fatalf("default: %s %s", ConfigDir(), StateDir())
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"gpu-box", "a", "laptop2"} {
		if ValidName(ok) != nil {
			t.Errorf("%s should be valid", ok)
		}
	}
	for _, bad := range []string{"", "-x", "Gpu", "a/b", "..", "a b"} {
		if ValidName(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestFromPeerSelfSkewAndIdentity(t *testing.T) {
	now := t0.Add(24 * time.Hour)
	held := Machine{Name: "gpu-box", TailscaleIP: "100.64.0.2", SSHHostKey: "ssh-ed25519 AAAAhost",
		SSHClientKeys: []string{"ssh-ed25519 AAAAkey"}, AddedAt: t0, UpdatedAt: t0, Notes: "old"}
	self := Machine{Name: "laptop", AddedAt: t0, UpdatedAt: t0, Notes: "mine"}
	local := Bundle{Machines: []Machine{held, self}}
	in := Bundle{
		Machines: []Machine{
			{Name: "gpu-box", TailscaleIP: "100.64.0.66", SSHHostKey: "ssh-ed25519 AAAAevil",
				SSHClientKeys: []string{"ssh-ed25519 AAAAevil"}, AddedAt: now, UpdatedAt: now, Notes: "new", Role: "gpu"},
			{Name: "laptop", AddedAt: now, UpdatedAt: now, Notes: "hijacked"},
			{Name: "future-a", UpdatedAt: now.Add(MaxSkew + time.Second)},
			{Name: "future-b", UpdatedAt: now, AddedAt: now.Add(time.Hour)},
			{Name: "near", UpdatedAt: now.Add(MaxSkew), AddedAt: now},
		},
		Tombstones: []Tombstone{
			{Name: "laptop", RemovedAt: now},
			{Name: "far", RemovedAt: now.Add(MaxSkew + time.Second)},
		},
	}
	got := Merge(local, FromPeer(local, in, "laptop", now))
	byName := map[string]Machine{}
	for _, m := range got.Machines {
		byName[m.Name] = m
	}
	g := byName["gpu-box"]
	if g.TailscaleIP != held.TailscaleIP || g.SSHHostKey != held.SSHHostKey || g.SSHClientKeys[0] != held.SSHClientKeys[0] || !g.AddedAt.Equal(t0) {
		t.Fatalf("identity changed through sync: %+v", g)
	}
	if g.Notes != "new" || g.Role != "gpu" || !g.UpdatedAt.Equal(now) {
		t.Fatalf("other fields should still merge: %+v", g)
	}
	if byName["laptop"].Notes != "mine" {
		t.Fatalf("self record changed by a peer: %+v", byName["laptop"])
	}
	if _, ok := byName["future-a"]; ok {
		t.Fatal("future updated_at accepted")
	}
	if _, ok := byName["future-b"]; ok {
		t.Fatal("future added_at accepted")
	}
	if _, ok := byName["near"]; !ok {
		t.Fatal("within the skew window should be accepted")
	}
	if len(got.Tombstones) != 0 {
		t.Fatalf("self or future tombstone accepted: %+v", got.Tombstones)
	}

	// a machine with no record of itself yet takes it from the first sync
	first := FromPeer(Bundle{}, Bundle{Machines: []Machine{self}}, "laptop", now)
	if len(first.Machines) != 1 {
		t.Fatalf("first self record should be taken: %+v", first)
	}
	// a pending record held here is finished by the peer's full one
	pend := Bundle{Machines: []Machine{{Name: "gpu-box", Pending: true, AddedAt: t0, UpdatedAt: t0}}}
	finished := held
	finished.UpdatedAt = now
	done := Merge(pend, FromPeer(pend, Bundle{Machines: []Machine{finished}}, "laptop", now))
	if done.Machines[0].SSHHostKey != held.SSHHostKey || done.Machines[0].Pending {
		t.Fatalf("pending record not finished: %+v", done.Machines[0])
	}
}

func TestMergeFromClearLost(t *testing.T) {
	for _, c := range []struct {
		sender string
		back   bool
	}{{"laptop", true}, {"gpu-box", false}} {
		s := &Store{Dir: t.TempDir()}
		if err := s.SetSelf("desk"); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveTombstone(Tombstone{Name: "gpu-box", RemovedAt: t0, Lost: true}); err != nil {
			t.Fatal(err)
		}
		readd := rec("gpu-box", time.Now().UTC(), "back")
		// without clear_lost the lost tombstone wins
		if got, _ := s.MergeFrom(Bundle{Machines: []Machine{readd}}, c.sender); len(got.Machines) != 0 {
			t.Fatalf("a re-add beat a lost tombstone through sync: %+v", got)
		}
		got, err := s.MergeFrom(Bundle{Machines: []Machine{readd}, ClearLost: []string{"gpu-box"}}, c.sender)
		if err != nil {
			t.Fatal(err)
		}
		if back := len(got.Machines) == 1 && len(got.Tombstones) == 0; back != c.back {
			t.Fatalf("sender %s: clear_lost honoured %v, want %v: %+v", c.sender, back, c.back, got)
		}
	}
}

func TestHeldKeysKeptLocally(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	m := rec("gpu-box", t0, "")
	m.SSHClientKeys = []string{"ssh-ed25519 AAAAheld"}
	if err := s.SaveMachine(m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MergeIn(Bundle{Tombstones: []Tombstone{{Name: "gpu-box", RemovedAt: t0.Add(time.Hour), Lost: true}}}); err != nil {
		t.Fatal(err)
	}
	tomb, _ := s.Tombstone("gpu-box")
	if len(tomb.HeldKeys) != 1 || tomb.HeldKeys[0] != "ssh-ed25519 AAAAheld" {
		t.Fatalf("held keys: %+v", tomb)
	}
	// a later merge keeps them; they never go over the wire
	if _, err := s.MergeIn(Bundle{Tombstones: []Tombstone{{Name: "gpu-box", RemovedAt: t0.Add(2 * time.Hour), Lost: true}}}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Bundle()
	if len(b.Tombstones[0].HeldKeys) != 1 {
		t.Fatalf("held keys lost on merge: %+v", b.Tombstones[0])
	}
	if data, _ := json.Marshal(b); strings.Contains(string(data), "AAAAheld") {
		t.Fatalf("held keys in sync json: %s", data)
	}
}

func TestRemoveDropsFutureRecord(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.SaveMachine(rec("gpu-box", t0.Add(100*365*24*time.Hour), "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(Tombstone{Name: "gpu-box", RemovedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Machine("gpu-box"); err == nil {
		t.Fatal("record should be gone")
	}
	if _, ok := s.Tombstone("gpu-box"); !ok {
		t.Fatal("tombstone missing")
	}
}

func TestIdentityRoundTrip(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecrets(map[string]any{"provider": "bws"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity("gpu-box", "~/.ssh/id_example"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity("desk", "/keys/desk"); err != nil {
		t.Fatal(err)
	}
	c, err := s.Config()
	if err != nil {
		t.Fatal(err)
	}
	if c.Self != "laptop" || c.Secrets["provider"] != "bws" || c.SSH["gpu-box"].Identity != "~/.ssh/id_example" {
		t.Fatalf("config %+v", c)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, "config.toml"))
	if !strings.Contains(string(raw), "[ssh.gpu-box]") || !strings.Contains(string(raw), "~/.ssh/id_example") {
		t.Fatalf("file:\n%s", raw)
	}
	// other writers keep it
	if err := s.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecrets(nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Identity("gpu-box"); got != "~/.ssh/id_example" {
		t.Fatalf("lost after rewrite: %q", got)
	}
	// clearing one leaves the other, clearing both drops the table
	if err := s.SetIdentity("gpu-box", ""); err != nil {
		t.Fatal(err)
	}
	if s.Identity("gpu-box") != "" || s.Identity("desk") != "/keys/desk" {
		t.Fatalf("after clear: %+v", s.Identity("desk"))
	}
	if err := s.SetIdentity("desk", ""); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(s.Dir, "config.toml"))
	if strings.Contains(string(raw), "ssh") {
		t.Fatalf("empty table left behind:\n%s", raw)
	}
	if err := s.SetIdentity("Bad Name", "x"); err == nil {
		t.Fatal("bad name should be refused")
	}
}

func TestExpandHome(t *testing.T) {
	t.Setenv("YONDER_HOME", t.TempDir())
	if got := ExpandHome("~/.ssh/k"); got != filepath.Join(UserHome(), ".ssh", "k") {
		t.Fatalf("got %q", got)
	}
	if ExpandHome("/abs/k") != "/abs/k" || ExpandHome("rel/k") != "rel/k" || ExpandHome("~other/k") != "~other/k" {
		t.Fatal("only a leading ~/ expands")
	}
}

// a peer's pending copy (init run elsewhere while the machine slept) must not
// unpin a set-up record; a held pin survives a pending copy too
func TestFromPeerNeverUnpins(t *testing.T) {
	now := t0.Add(24 * time.Hour)
	setUp := Machine{Name: "gpu-box", TailscaleIP: "100.64.0.2", SSHHostKey: "ssh-ed25519 AAAAhost",
		SSHClientKeys: []string{"ssh-ed25519 AAAAkey"}, YonderPath: "/home/me/.local/bin/yonder", AddedAt: t0, UpdatedAt: t0}
	pend := Machine{Name: "gpu-box", TailscaleIP: "100.64.0.2", Pending: true, SSHClientKeys: []string{}, AddedAt: now, UpdatedAt: now}

	got := Merge(Bundle{Machines: []Machine{setUp}}, FromPeer(Bundle{Machines: []Machine{setUp}}, Bundle{Machines: []Machine{pend}}, "laptop", now))
	if g := got.Machines[0]; g.Pending || g.SSHHostKey != setUp.SSHHostKey || g.YonderPath == "" {
		t.Fatalf("set-up record turned pending through sync: %+v", g)
	}

	// pending with a held pin, from `add` while it slept: the pin stays
	heldPin := pend
	heldPin.SSHHostKey, heldPin.UpdatedAt = setUp.SSHHostKey, t0
	got = Merge(Bundle{Machines: []Machine{heldPin}}, FromPeer(Bundle{Machines: []Machine{heldPin}}, Bundle{Machines: []Machine{pend}}, "laptop", now))
	if g := got.Machines[0]; g.SSHHostKey != setUp.SSHHostKey {
		t.Fatalf("held pin dropped: %+v", g)
	}

	// pending with no pin here, a peer has the set-up record: it wins even though older
	got = Merge(Bundle{Machines: []Machine{pend}}, FromPeer(Bundle{Machines: []Machine{pend}}, Bundle{Machines: []Machine{setUp}}, "laptop", now))
	if g := got.Machines[0]; g.Pending || g.SSHHostKey != setUp.SSHHostKey {
		t.Fatalf("full record should finish the pending one: %+v", g)
	}
}
