package store

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testKind = Kind{
	Name: "notes",
	Ignore: func(rel string, dir bool) bool {
		return dir && filepath.Base(rel) == ".git"
	},
	Check: func(rel string, data []byte) *Refusal {
		if strings.HasSuffix(rel, ".key") {
			return &Refusal{Reason: "the name looks like a secret"}
		}
		if bytes.Contains(data, []byte("-----BEGIN PRIVATE KEY")) {
			return &Refusal{Reason: "looks like a private key", Fix: "remove it"}
		}
		return nil
	},
	Review: func(rel string, exec bool) bool { return exec || strings.HasPrefix(rel, "hooks/") },
}

// machine is one fake fleet machine's store, with its own clock.
type machine struct {
	t   *testing.T
	s   *Store
	now time.Time
}

func newMachine(t *testing.T, name string) *machine {
	root := t.TempDir()
	m := &machine{t: t, now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
	m.s = &Store{Kind: testKind, Dir: filepath.Join(root, "data"), StateDir: filepath.Join(root, "state"), Self: name}
	m.s.Now = func() time.Time { return m.now }
	return m
}

func (m *machine) write(rel, content string, mtime time.Time) {
	m.t.Helper()
	p := filepath.Join(m.s.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(m.s.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// locked runs fn under the lock between a scan and a save, like a command does.
func (m *machine) locked(fn func()) {
	m.t.Helper()
	must(m.t, m.s.Lock(time.Second))
	defer m.s.Unlock()
	_, _, err := m.s.Scan()
	must(m.t, err)
	fn()
	_, _, err = m.s.Scan()
	must(m.t, err)
	must(m.t, m.s.Save())
}

// peer runs the target side of another machine in process.
type peer struct{ m *machine }

func (p peer) Name() string { return p.m.s.Self }
func (p peer) Offer() (Manifest, error) {
	var out bytes.Buffer
	if err := ServeOffer(p.m.s, &out); err != nil {
		return Manifest{}, err
	}
	var mf Manifest
	return mf, json.Unmarshal(out.Bytes(), &mf)
}
func (p peer) Fetch(paths []string) (Bundle, error) {
	req, _ := json.Marshal(fetchRequest{Paths: paths})
	var out bytes.Buffer
	if err := ServeSend(p.m.s, bytes.NewReader(req), &out); err != nil {
		return Bundle{}, err
	}
	return ReadBundle(&out)
}
func (p peer) Push(b Bundle) (PushResult, error) {
	var in, out bytes.Buffer
	if err := WriteBundle(&in, b); err != nil {
		return PushResult{}, err
	}
	if err := ServeRecv(p.m.s, &in, &out, ""); err != nil {
		return PushResult{}, err
	}
	var r PushResult
	return r, json.Unmarshal(out.Bytes(), &r)
}

// sync runs one full sync from m with other, the way `yonder sync` does.
func (m *machine) sync(other *machine) Report {
	m.t.Helper()
	if err := m.s.Lock(time.Second); err != nil {
		m.t.Fatal(err)
	}
	defer m.s.Unlock()
	if _, _, err := m.s.Scan(); err != nil {
		m.t.Fatal(err)
	}
	r, err := m.s.SyncWith(peer{other}, false)
	if err != nil {
		m.t.Fatalf("sync %s -> %s: %v", m.s.Self, other.s.Self, err)
	}
	return r
}

func TestVectorCompare(t *testing.T) {
	a := Vector{"laptop": 2, "gpu-box": 1}
	cases := []struct {
		b    Vector
		want Order
	}{
		{Vector{"laptop": 2, "gpu-box": 1}, Equal},
		{Vector{"laptop": 1, "gpu-box": 1}, After},
		{Vector{"laptop": 2, "gpu-box": 1, "server": 1}, Before},
		{Vector{"laptop": 3}, Concurrent},
		{nil, After},
	}
	for _, c := range cases {
		if got := a.Compare(c.b); got != c.want {
			t.Errorf("%v vs %v: got %d want %d", a, c.b, got, c.want)
		}
	}
	if got := (Vector{}).Compare(Vector{"x": 0}); got != Equal {
		t.Errorf("zero counters are equal to missing: %d", got)
	}
	if b := a.Bump("laptop"); b["laptop"] != 3 || a["laptop"] != 2 {
		t.Errorf("bump must copy: %v %v", a, b)
	}
}

func TestSyncTwoMachines(t *testing.T) {
	a, b := newMachine(t, "laptop"), newMachine(t, "gpu-box")
	a.write("instructions.md", "be brief\n", a.now.Add(-time.Hour))
	a.write("skills/x/SKILL.md", "skill x\n", a.now.Add(-time.Hour))
	a.write(".git/HEAD", "ref\n", a.now)
	r := a.sync(b)
	if len(r.Sent) != 2 || b.read("instructions.md") != "be brief\n" || b.read("skills/x/SKILL.md") != "skill x\n" {
		t.Fatalf("first sync: %+v", r)
	}
	if b.read(".git/HEAD") != "<missing>" {
		t.Fatal(".git must never sync")
	}
	if r := a.sync(b); r.Changed() {
		t.Fatalf("second sync should be a no-op: %+v", r)
	}

	// an edit and a delete on gpu-box come back to laptop
	b.now = b.now.Add(time.Minute)
	b.write("instructions.md", "be brief. really.\n", b.now)
	os.RemoveAll(filepath.Join(b.s.Dir, "skills"))
	r = a.sync(b)
	if a.read("instructions.md") != "be brief. really.\n" || a.read("skills/x/SKILL.md") != "<missing>" || len(r.Got) != 2 {
		t.Fatalf("pull edit and delete: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(a.s.Dir, "skills")); !os.IsNotExist(err) {
		t.Fatal("empty folders left by a delete should go")
	}
	for _, g := range r.Got {
		if g.State.Origin.Machine != "gpu-box" {
			t.Fatalf("origin should be gpu-box: %+v", g)
		}
	}
	// laptop kept what the sync replaced and deleted
	vs, _ := a.s.History("instructions.md")
	if len(vs) != 1 {
		t.Fatalf("history: %+v", vs)
	}
	if d, _ := vs[0].Data(); string(d) != "be brief\n" {
		t.Fatalf("kept version: %q", d)
	}
	if vs, _ := a.s.History("skills/x/SKILL.md"); len(vs) != 1 {
		t.Fatalf("deleted file should be kept: %+v", vs)
	}
	log, _ := a.s.Log("instructions.md")
	if len(log) != 2 || log[0].Action != "edit" || log[0].Origin.Machine != "laptop" || log[1].From != "gpu-box" || log[1].Origin.Machine != "gpu-box" {
		t.Fatalf("log: %+v", log)
	}

	// restore puts the old text back; the next sync sends it as an edit here
	if err := a.s.Lock(time.Second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.s.Restore("instructions.md", 1); err != nil {
		t.Fatal(err)
	}
	a.s.Unlock()
	a.sync(b)
	if b.read("instructions.md") != "be brief\n" {
		t.Fatalf("restore should spread: %q", b.read("instructions.md"))
	}
}

func TestConcurrentEditKeepsBoth(t *testing.T) {
	a, b := newMachine(t, "laptop"), newMachine(t, "gpu-box")
	a.write("instructions.md", "v1\n", a.now.Add(-time.Hour))
	a.sync(b)
	a.write("instructions.md", "laptop edit\n", a.now.Add(-10*time.Minute))
	b.write("instructions.md", "gpu edit\n", b.now.Add(-5*time.Minute)) // later: wins
	r := a.sync(b)
	if len(r.Conflicts) != 1 || r.Conflicts[0].Winner != "gpu-box" || r.Conflicts[0].Loser != "laptop" {
		t.Fatalf("conflict: %+v", r)
	}
	cp := r.Conflicts[0].Copy
	if cp != "instructions.conflict-laptop-20261112.md" {
		t.Fatalf("copy name: %s", cp)
	}
	for _, m := range []*machine{a, b} {
		if m.read("instructions.md") != "gpu edit\n" || m.read(cp) != "laptop edit\n" {
			t.Fatalf("%s: %q / %q", m.s.Self, m.read("instructions.md"), m.read(cp))
		}
	}
	if r := b.sync(a); r.Changed() {
		t.Fatalf("both sides should agree after a conflict: %+v", r)
	}
	if base, mach, ok := ConflictOf(cp); !ok || base != "instructions.md" || mach != "laptop" {
		t.Fatalf("ConflictOf: %s %s %v", base, mach, ok)
	}
	if cs, _ := a.s.Conflicts(); len(cs["instructions.md"]) != 1 {
		t.Fatalf("conflicts: %+v", cs)
	}

	// laptop keeps its own version; the copy goes everywhere
	if err := a.s.Lock(time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := a.s.State(); err != nil {
		t.Fatal(err)
	}
	kept, removed, err := a.s.Resolve("instructions.md", "mine")
	a.s.Unlock()
	if err != nil || kept != "laptop" || len(removed) != 1 {
		t.Fatalf("resolve: %s %v %v", kept, removed, err)
	}
	a.now = a.now.Add(time.Minute)
	a.sync(b)
	for _, m := range []*machine{a, b} {
		if m.read("instructions.md") != "laptop edit\n" || m.read(cp) != "<missing>" {
			t.Fatalf("%s after resolve: %q / %q", m.s.Self, m.read("instructions.md"), m.read(cp))
		}
	}
}

func TestDeleteAgainstEditKeepsEdit(t *testing.T) {
	a, b := newMachine(t, "laptop"), newMachine(t, "gpu-box")
	a.write("notes.md", "v1\n", a.now.Add(-time.Hour))
	a.sync(b)
	os.Remove(filepath.Join(a.s.Dir, "notes.md"))
	b.write("notes.md", "v2\n", b.now)
	r := a.sync(b)
	if len(r.DeleteLost) != 1 || a.read("notes.md") != "v2\n" || b.read("notes.md") != "v2\n" {
		t.Fatalf("edit should win: %+v %q", r, a.read("notes.md"))
	}
	st, _ := a.s.State()
	if len(st.Notices) == 0 || st.Notices[len(st.Notices)-1].Kind != "delete-lost" {
		t.Fatalf("notice: %+v", st.Notices)
	}
}

func TestOfflineCatchUpThroughAnotherMachine(t *testing.T) {
	a, b, c := newMachine(t, "laptop"), newMachine(t, "gpu-box"), newMachine(t, "server")
	a.write("instructions.md", "v1\n", a.now)
	a.sync(b)
	a.sync(c)
	// gpu-box is asleep while laptop edits, then laptop goes away
	a.write("instructions.md", "v2\n", a.now.Add(time.Minute))
	a.sync(c)
	if b.read("instructions.md") != "v1\n" {
		t.Fatal("gpu-box was offline")
	}
	// gpu-box wakes and syncs with server only
	b.sync(c)
	if b.read("instructions.md") != "v2\n" {
		t.Fatalf("gpu-box should catch up through server: %q", b.read("instructions.md"))
	}
}

func TestReviewTier(t *testing.T) {
	a, b := newMachine(t, "laptop"), newMachine(t, "gpu-box")
	a.write("hooks/guard.sh", "#!/bin/sh\necho v1\n", a.now)
	a.write("instructions.md", "text\n", a.now)
	r := a.sync(b)
	if len(r.PeerPending) != 1 || r.PeerPending[0] != "hooks/guard.sh" {
		t.Fatalf("a new hook should wait for review there: %+v", r)
	}
	if b.read("hooks/guard.sh") != "<missing>" || b.read("instructions.md") != "text\n" {
		t.Fatal("the hook must not be live before accept; plain text is")
	}
	// gpu-box relays the waiting version to a third machine as the store's version
	c := newMachine(t, "server")
	b.sync(c)
	if items, _ := c.s.PendingList(); len(items) != 1 {
		t.Fatalf("relayed hook should wait on server too: %+v", items)
	}
	// a rescan with nothing live doesn't count as a delete
	if r := b.sync(a); r.Changed() {
		t.Fatalf("pending must not look like a local change: %+v", r)
	}
	live, in, err := b.s.PendingData("hooks/guard.sh")
	if err != nil || live != nil || string(in) != "#!/bin/sh\necho v1\n" {
		t.Fatalf("pending data: %q %q %v", live, in, err)
	}
	if err := b.s.Lock(time.Second); err != nil {
		t.Fatal(err)
	}
	if err := b.s.Accept("hooks/guard.sh"); err != nil {
		t.Fatal(err)
	}
	if err := b.s.Save(); err != nil {
		t.Fatal(err)
	}
	b.s.Unlock()
	if b.read("hooks/guard.sh") != "#!/bin/sh\necho v1\n" {
		t.Fatal("accept should make it live")
	}
	// a change made here is never held for review here
	b.write("hooks/guard.sh", "#!/bin/sh\necho mine\n", b.now.Add(time.Minute))
	if r := b.sync(a); len(r.PeerPending) != 1 {
		t.Fatalf("the change waits on laptop: %+v", r)
	}
	// laptop edits the hook while gpu-box's change waits: the local edit wins
	a.write("hooks/guard.sh", "#!/bin/sh\necho override\n", a.now.Add(2*time.Minute))
	a.locked(func() {})
	items, _ := a.s.PendingList()
	if len(items) != 0 {
		t.Fatalf("a local edit replaces what waited: %+v", items)
	}
}

func TestRefusals(t *testing.T) {
	a, b := newMachine(t, "laptop"), newMachine(t, "gpu-box")
	a.write("deploy.key", "x", a.now)
	a.write("notes/creds.md", "-----BEGIN PRIVATE KEY-----\nabc\n", a.now)
	a.write("fine.md", "ok", a.now)
	must(t, os.Symlink("fine.md", filepath.Join(a.s.Dir, "link.md")))
	must(t, a.s.Lock(time.Second))
	_, refused, err := a.s.Scan()
	a.s.Unlock()
	if err != nil || len(refused) != 3 {
		t.Fatalf("refused: %+v %v", refused, err)
	}
	a.sync(b)
	if b.read("deploy.key") != "<missing>" || b.read("notes/creds.md") != "<missing>" || b.read("link.md") != "<missing>" || b.read("fine.md") != "ok" {
		t.Fatal("only fine.md should sync")
	}
	// a content match can be waived for that exact content; a name can't
	a.locked(func() {
		if err := a.s.Allow("deploy.key"); err == nil {
			t.Fatal("a secret name can't be allowed")
		}
		if err := a.s.Allow("notes/creds.md"); err != nil {
			t.Fatal(err)
		}
	})
	a.sync(b)
	if b.read("notes/creds.md") == "<missing>" {
		t.Fatal("an allowed file should sync")
	}

	// the receiver checks too: a bundle carrying a key is refused there
	bad := Bundle{Store: "notes", From: "laptop", Files: map[string]FileState{
		"x.md": {Hash: hashOf([]byte("-----BEGIN PRIVATE KEY")), Version: Vector{"laptop": 1}, Origin: Origin{Machine: "laptop"}},
	}, Data: map[string][]byte{"x.md": []byte("-----BEGIN PRIVATE KEY")}}
	res, err := peer{b}.Push(bad)
	if err != nil || len(res.Refused) != 1 || b.read("x.md") != "<missing>" {
		t.Fatalf("receiver check: %+v %v", res, err)
	}
}

func TestReadBundleRefusesBadEntries(t *testing.T) {
	mk := func(meta Bundle, name string, data []byte) *bytes.Buffer {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		m, _ := json.Marshal(meta)
		must(t, tw.WriteHeader(&tar.Header{Name: bundleMeta, Size: int64(len(m)), Mode: 0o600}))
		_, err := tw.Write(m)
		must(t, err)
		must(t, tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0o644, Typeflag: tar.TypeReg}))
		_, err = tw.Write(data)
		must(t, err)
		must(t, tw.Close())
		return &buf
	}
	good := []byte("hi")
	ok := Bundle{V: 1, Store: "notes", Files: map[string]FileState{"a.md": {Hash: hashOf(good)}}}
	if _, err := ReadBundle(mk(ok, "a.md", good)); err != nil {
		t.Fatalf("good bundle: %v", err)
	}
	cases := map[string]*bytes.Buffer{
		"traversal": mk(Bundle{V: 1, Files: map[string]FileState{"../a.md": {Hash: hashOf(good)}}}, "../a.md", good),
		"unlisted":  mk(ok, "b.md", good),
		"hash":      mk(ok, "a.md", []byte("no")),
		"version":   mk(Bundle{V: 99, Files: ok.Files}, "a.md", good),
	}
	for name, buf := range cases {
		if _, err := ReadBundle(buf); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
}

func TestLockBusy(t *testing.T) {
	a := newMachine(t, "laptop")
	if err := a.s.Lock(time.Second); err != nil {
		t.Fatal(err)
	}
	defer a.s.Unlock()
	other := *a.s
	other.lock, other.st = nil, nil
	if err := other.Lock(100 * time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock should be busy: %v", err)
	}
}

func TestConflictName(t *testing.T) {
	day := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"skills/x/SKILL.md": "skills/x/SKILL.conflict-gpu-box-20261112.md",
		"hooks/guard":       "hooks/guard.conflict-gpu-box-20261112",
		".editorconfig":     ".editorconfig.conflict-gpu-box-20261112",
	}
	for in, want := range cases {
		if got := ConflictName(in, "gpu-box", day, 1); got != want {
			t.Errorf("%s: %s", in, got)
		}
		if base, m, ok := ConflictOf(want); !ok || base != in || m != "gpu-box" {
			t.Errorf("ConflictOf(%s) = %s %s %v", want, base, m, ok)
		}
	}
	if got := ConflictName("a.md", "x1", day, 2); got != "a.conflict-x1-20261112-2.md" {
		t.Errorf("second copy: %s", got)
	}
}
