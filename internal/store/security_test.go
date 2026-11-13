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

// trySync is sync that returns the error instead of failing the test.
func (m *machine) trySync(other *machine) (Report, error) {
	m.t.Helper()
	must(m.t, m.s.Lock(time.Second))
	defer m.s.Unlock()
	if _, _, err := m.s.Scan(); err != nil {
		return Report{}, err
	}
	must(m.t, m.s.Save())
	return m.s.SyncWith(peer{other, m.s.Self}, false)
}

// push sends a hand-made bundle to m, the way _store-recv gets one.
func push(t *testing.T, m *machine, b Bundle) PushResult {
	t.Helper()
	var in, out bytes.Buffer
	must(t, WriteBundle(&in, b))
	must(t, ServeRecv(m.s, &in, &out, b.From))
	var r PushResult
	must(t, json.Unmarshal(out.Bytes(), &r))
	return r
}

func file(content, from string, at time.Time) FileState {
	return FileState{Hash: hashOf([]byte(content)), Size: int64(len(content)), Version: Vector{from: 1}, Origin: Origin{Machine: from, Time: at}}
}

func TestScanStopsWhenStoreFolderIsMissing(t *testing.T) {
	a, b := newMachine(t, "laptop"), newMachine(t, "gpu-box")
	a.write("instructions.md", "rules\n", a.now)
	a.write("skills/x/SKILL.md", "x\n", a.now)
	a.sync(b)
	must(t, os.RemoveAll(a.s.Dir))
	if _, err := a.trySync(b); !errors.Is(err, ErrMassDelete) || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("a missing folder should stop the sync: %v", err)
	}
	if b.read("instructions.md") != "rules\n" {
		t.Fatal("nothing may be deleted on the other machine")
	}
}

func TestScanReadsThroughSymlinkedStoreFolder(t *testing.T) {
	a, b := newMachine(t, "laptop"), newMachine(t, "gpu-box")
	a.write("instructions.md", "rules\n", a.now)
	a.write("skills/x/SKILL.md", "x\n", a.now)
	a.sync(b)
	moved := filepath.Join(t.TempDir(), "dotfiles-agents")
	must(t, os.Rename(a.s.Dir, moved))
	must(t, os.Symlink(moved, a.s.Dir))
	r, err := a.trySync(b)
	if err != nil || r.Changed() {
		t.Fatalf("a store folder moved and linked back is the same store: %+v %v", r, err)
	}
	if b.read("instructions.md") != "rules\n" || b.read("skills/x/SKILL.md") != "x\n" {
		t.Fatal("the other machine must keep its files")
	}
}

func TestScanStopsOnMassDelete(t *testing.T) {
	a, b := newMachine(t, "laptop"), newMachine(t, "gpu-box")
	for i := range 10 {
		a.write("skills/s"+string(rune('a'+i))+"/SKILL.md", "x\n", a.now)
	}
	a.sync(b)
	for i := range 6 {
		must(t, os.RemoveAll(filepath.Join(a.s.Dir, "skills", "s"+string(rune('a'+i)))))
	}
	held, err := a.s.Held()
	if err != nil || !strings.Contains(held, "6 of the 10 files") {
		t.Fatalf("doctor's check sees it: %q %v", held, err)
	}
	a.s.Unlock()
	if _, err := a.trySync(b); !errors.Is(err, ErrMassDelete) || !strings.Contains(err.Error(), "6 of the 10") {
		t.Fatalf("deleting more than half should stop: %v", err)
	}
	if b.read("skills/sa/SKILL.md") != "x\n" {
		t.Fatal("nothing may be deleted on the other machine")
	}
	a.s.AllowDeletes = true
	if _, err := a.trySync(b); err != nil {
		t.Fatal(err)
	}
	if b.read("skills/sa/SKILL.md") != "<missing>" || b.read("skills/sj/SKILL.md") != "x\n" {
		t.Fatal("--allow-deletes should go ahead")
	}
	for _, c := range []struct {
		deletes, tracked int
		trips            bool
	}{{1, 1, false}, {1, 2, false}, {2, 2, true}, {2, 3, true}, {4, 9, false}, {5, 9, true}, {8, 9, true}, {5, 10, false}, {6, 10, true}} {
		if massDelete(c.deletes, c.tracked) != c.trips {
			t.Errorf("guard for %d of %d: want %v", c.deletes, c.tracked, c.trips)
		}
	}
}

func TestMassDeleteGuardCatchesSmallStores(t *testing.T) {
	a, b := newMachine(t, "alpha"), newMachine(t, "bravo")
	for i := range 9 {
		a.write("skills/s"+string(rune('a'+i))+"/SKILL.md", "x\n", a.now)
	}
	a.sync(b)
	for i := range 8 {
		must(t, os.RemoveAll(filepath.Join(a.s.Dir, "skills", "s"+string(rune('a'+i)))))
	}
	if _, err := a.trySync(b); !errors.Is(err, ErrMassDelete) {
		t.Fatalf("8 of 9 gone must stop: %v", err)
	}
	if b.read("skills/sa/SKILL.md") != "x\n" {
		t.Fatal("nothing spreads")
	}
}

func TestAllowedFileStaysAllowedOnReceiver(t *testing.T) {
	b := newMachine(t, "gpu-box")
	c := "-----BEGIN PRIVATE KEY----- (a test fixture, not a key)\n"
	f := file(c, "laptop", b.now)
	f.Allowed = true
	r := push(t, b, Bundle{Store: "notes", From: "laptop", Files: map[string]FileState{"fixture.md": f}, Data: map[string][]byte{"fixture.md": []byte(c)}})
	if len(r.Applied) != 1 {
		t.Fatalf("an allowed file should be taken: %+v", r)
	}
	must(t, b.s.Lock(time.Second))
	defer b.s.Unlock()
	if ref, err := b.s.Refusals(); err != nil || len(ref) != 0 {
		t.Fatalf("the receiver's own scan must take it too: %v %v", ref, err)
	}
}

func TestConflictKeepsBothWhenCopyCannotBeWritten(t *testing.T) {
	k := testKind
	k.Check = func(rel string, _ []byte) *Refusal {
		if strings.Contains(rel, "secret") {
			return &Refusal{Reason: "the name looks like a secret", Final: true}
		}
		return nil
	}
	a, b := newMachine(t, "secret-box"), newMachine(t, "laptop")
	a.s.Kind, b.s.Kind = k, k
	a.write("notes.md", "base\n", a.now)
	a.sync(b)
	a.write("notes.md", "older edit on secret-box\n", a.now.Add(time.Minute))
	b.write("notes.md", "newer edit on laptop\n", a.now.Add(2*time.Minute))
	r := a.sync(b)
	if len(r.Conflicts) != 0 || !strings.Contains(skipped(r), "could not be written") {
		t.Fatalf("the failed copy should be reported: %+v", r)
	}
	if a.read("notes.md") != "older edit on secret-box\n" || b.read("notes.md") != "newer edit on laptop\n" {
		t.Fatalf("both edits must survive: %q / %q", a.read("notes.md"), b.read("notes.md"))
	}
}

func skipped(r Report) string {
	var s []string
	for _, k := range r.Skipped {
		s = append(s, k.Path+": "+k.Why)
	}
	return strings.Join(s, "\n")
}

func TestReceiveChecksStoreLimits(t *testing.T) {
	b := newMachine(t, "gpu-box")
	big := strings.Repeat("x", MaxFile)
	applied, refused := 0, 0
	for round := range 2 {
		bn := Bundle{Store: "notes", From: "laptop", Files: map[string]FileState{}, Data: map[string][]byte{}}
		for i := range 30 {
			rel := "f" + string(rune('a'+round)) + string(rune('a'+i)) + ".md"
			bn.Files[rel] = file(big, "laptop", b.now)
			bn.Data[rel] = []byte(big)
		}
		r := push(t, b, bn)
		applied += len(r.Applied)
		refused += len(r.Refused)
	}
	if applied > MaxTotal/MaxFile || refused == 0 {
		t.Fatalf("the store total must hold on receive: applied %d, refused %d", applied, refused)
	}

	old := MaxFiles
	MaxFiles = 3
	defer func() { MaxFiles = old }()
	c := newMachine(t, "server")
	bn := Bundle{Store: "notes", From: "laptop", Files: map[string]FileState{}, Data: map[string][]byte{}}
	for _, n := range []string{"a.md", "b.md", "c.md", "d.md"} {
		bn.Files[n] = file(n, "laptop", c.now)
		bn.Data[n] = []byte(n)
	}
	if r := push(t, c, bn); len(r.Applied) != 3 || len(r.Refused) != 1 {
		t.Fatalf("the file count must hold on receive: %+v", r)
	}
}

func TestRestoreOfHookWaitsForReview(t *testing.T) {
	a, b := newMachine(t, "laptop"), newMachine(t, "gpu-box")
	b.write("hooks/x.sh", "old safe hook\n", b.now)
	b.sync(a)
	a.write("hooks/x.sh", "incoming, never accepted\n", a.now.Add(time.Minute))
	a.sync(b)
	// an edit here drops the waiting change into history
	b.write("hooks/x.sh", "old safe hook, edited\n", b.now.Add(2*time.Minute))
	b.locked(func() {
		vs, err := b.s.History("hooks/x.sh")
		must(t, err)
		if len(vs) == 0 || !strings.Contains(vs[0].Why, "waited for review") {
			t.Fatalf("history: %+v", vs)
		}
		_, pending, err := b.s.Restore("hooks/x.sh", 1)
		must(t, err)
		if !pending {
			t.Fatal("a restored hook must wait for review")
		}
	})
	if b.read("hooks/x.sh") != "old safe hook, edited\n" {
		t.Fatalf("restore must not make the hook live: %q", b.read("hooks/x.sh"))
	}
	if items, _ := b.s.PendingList(); len(items) != 1 {
		t.Fatalf("it waits for accept: %+v", items)
	}
}

func TestReviewIgnoresClaimedOrigin(t *testing.T) {
	b := newMachine(t, "gpu-box")
	f := file("#!/bin/sh\nevil\n", "laptop", b.now)
	f.Origin.Machine = "gpu-box" // the sender writes this field
	r := push(t, b, Bundle{Store: "notes", From: "laptop", Files: map[string]FileState{"hooks/x.sh": f}, Data: map[string][]byte{"hooks/x.sh": []byte("#!/bin/sh\nevil\n")}})
	if len(r.Pending) != 1 || b.read("hooks/x.sh") != "<missing>" {
		t.Fatalf("a hook claiming to come from here still waits: %+v", r)
	}
}

// at sets every machine's clock.
func at(t time.Time, ms ...*machine) {
	for _, m := range ms {
		m.now = t
	}
}

func TestStaleMachineCannotBringBackDeletedFiles(t *testing.T) {
	a, b, d := newMachine(t, "laptop"), newMachine(t, "gpu-box"), newMachine(t, "server")
	t0 := a.now
	a.write("old.md", "old\n", t0)
	a.sync(b)
	// laptop deletes old.md and keeps syncing with server; gpu-box sleeps
	must(t, os.Remove(filepath.Join(a.s.Dir, "old.md")))
	for day := 0; day <= 100; day += 30 {
		at(t0.Add(time.Duration(day)*24*time.Hour), a, d)
		a.sync(d)
	}
	at(t0.Add(100*24*time.Hour), a, b, d)
	a.write("new.md", "new\n", a.now)
	a.locked(func() {
		if _, ok := a.s.st.Files["old.md"]; ok {
			t.Fatal("the marker should have expired")
		}
	})

	r := a.sync(b) // laptop syncs with the returning machine
	if a.read("old.md") != "<missing>" || b.read("new.md") != "new\n" || !strings.Contains(skipped(r), "held back") {
		t.Fatalf("laptop must not take old.md back, and gpu-box still takes what is new: %+v", r)
	}
	for range 2 { // and not on any later sync, either way round
		if r := b.sync(a); a.read("old.md") != "<missing>" || !strings.Contains(skipped(r), "held back") {
			t.Fatalf("gpu-box's push must not bring old.md back: %+v", r)
		}
		a.sync(b)
		if a.read("old.md") != "<missing>" {
			t.Fatal("nor laptop's pull")
		}
	}
	// an edit on gpu-box is no resurrection, so it goes
	at(a.now.Add(time.Hour), a, b)
	b.write("old.md", "old, edited\n", b.now)
	b.sync(a)
	if a.read("old.md") != "old, edited\n" {
		t.Fatalf("an edit made after the pair met is taken: %q", a.read("old.md"))
	}
}

func TestStaleOKSyncsHeldFiles(t *testing.T) {
	a, b := newMachine(t, "laptop"), newMachine(t, "gpu-box")
	t0 := a.now
	b.write("old.md", "old\n", t0)
	a.sync(b)
	must(t, os.Remove(filepath.Join(a.s.Dir, "old.md")))
	a.write("keep.md", "x\n", t0)
	a.locked(func() {}) // laptop records the delete; gpu-box never hears of it
	at(t0.Add(200*24*time.Hour), a, b)
	a.locked(func() {}) // the marker expires
	b.sync(a)
	if a.read("old.md") != "<missing>" {
		t.Fatal("held back")
	}
	b.s.StaleOK = true
	b.sync(a)
	if a.read("old.md") != "old\n" {
		t.Fatal("--stale-ok syncs it")
	}
}

func TestIdleFleetStillSyncsANewMachine(t *testing.T) {
	a, b, c := newMachine(t, "alpha"), newMachine(t, "bravo"), newMachine(t, "charlie")
	t0 := a.now
	a.write("instructions.md", "rules\n", t0)
	a.write("skills/x/SKILL.md", "x\n", t0)
	a.sync(b)
	at(t0.Add(100*24*time.Hour), a, b, c)
	for _, r := range []Report{a.sync(b), b.sync(a)} {
		if len(r.Skipped) != 0 {
			t.Fatalf("a pair that holds the same files holds nothing back: %+v", r)
		}
	}
	at(t0.Add(101*24*time.Hour), a, b, c)
	if r := a.sync(c); len(r.Skipped) != 0 || c.read("instructions.md") != "rules\n" {
		t.Fatalf("a new machine gets everything: %+v", r)
	}
	if r := c.sync(b); len(r.Skipped) != 0 {
		t.Fatalf("and nothing is held between it and the others: %+v", r)
	}
}

func TestMachinesAwayTogetherCannotBringBackADelete(t *testing.T) {
	a, b, c, d := newMachine(t, "alpha"), newMachine(t, "bravo"), newMachine(t, "charlie"), newMachine(t, "delta")
	t0 := a.now
	a.write("old.md", "old\n", t0)
	a.write("keep.md", "keep\n", t0)
	a.sync(b)
	a.sync(c)
	must(t, os.Remove(filepath.Join(a.s.Dir, "old.md")))
	a.sync(d) // alpha records the delete and stays in touch with delta
	for day := 30; day <= 90; day += 30 {
		at(t0.Add(time.Duration(day)*24*time.Hour), a, b, c, d)
		b.sync(c) // bravo and charlie keep each other fresh, away from alpha
		a.sync(d)
	}
	at(t0.Add(100*24*time.Hour), a, b, c, d)
	a.locked(func() {})
	if r := b.sync(a); a.read("old.md") != "<missing>" || !strings.Contains(skipped(r), "held back") {
		t.Fatalf("bravo must not bring old.md back to alpha: %+v", r)
	}
	if r := a.sync(c); a.read("old.md") != "<missing>" || !strings.Contains(skipped(r), "held back") {
		t.Fatalf("nor charlie: %+v", r)
	}
}

func TestFreshMachinePushesItsDeleteToAStalePeer(t *testing.T) {
	a, b := newMachine(t, "alpha"), newMachine(t, "bravo")
	t0 := a.now
	a.write("old.md", "old\n", t0)
	a.sync(b)
	at(t0.Add(95*24*time.Hour), a, b)
	must(t, os.Remove(filepath.Join(a.s.Dir, "old.md")))
	a.sync(b) // the pair is stale, but both know the path
	if b.read("old.md") != "<missing>" {
		t.Fatal("a delete marker both sides can match goes through")
	}
}

func TestWaitingChangeStaysWaitingWhenItWinsAConflict(t *testing.T) {
	v, a, c := newMachine(t, "victim"), newMachine(t, "alpha"), newMachine(t, "charlie")
	t0 := v.now
	v.write("hooks/x.sh", "old safe hook\n", t0)
	v.sync(a)
	v.sync(c)
	c.write("hooks/x.sh", "unreviewed from charlie\n", t0.Add(2*time.Minute))
	c.sync(v) // waits for review on victim
	a.write("hooks/x.sh", "alpha edit, older\n", t0.Add(time.Minute))
	r := v.sync(a) // victim is the caller; charlie's waiting version wins
	if len(r.Conflicts) != 1 {
		t.Fatalf("conflict: %+v", r)
	}
	if v.read("hooks/x.sh") != "old safe hook\n" {
		t.Fatalf("the waiting change must not go live: %q", v.read("hooks/x.sh"))
	}
	if !waits(t, v, "hooks/x.sh") {
		t.Fatal("it still waits")
	}
}

func waits(t *testing.T, m *machine, rel string) bool {
	t.Helper()
	items, err := m.s.PendingList()
	must(t, err)
	m.s.Unlock()
	for _, it := range items {
		if it.Path == rel {
			return true
		}
	}
	return false
}

func TestWaitingChangeStaysWaitingWhenItBeatsADelete(t *testing.T) {
	v, a, c := newMachine(t, "victim"), newMachine(t, "alpha"), newMachine(t, "charlie")
	t0 := v.now
	v.write("hooks/x.sh", "old safe hook\n", t0)
	v.sync(a)
	v.sync(c)
	c.write("hooks/x.sh", "unreviewed from charlie\n", t0.Add(2*time.Minute))
	c.sync(v)
	a.locked(func() { must(t, a.s.Accept("hooks/x.sh")) }) // live on alpha, which then deletes it
	must(t, os.Remove(filepath.Join(a.s.Dir, "hooks/x.sh")))
	r := v.sync(a) // the waiting edit beats alpha's delete
	if len(r.DeleteLost) != 1 || v.read("hooks/x.sh") != "old safe hook\n" {
		t.Fatalf("the waiting change must not go live: %+v %q", r, v.read("hooks/x.sh"))
	}
	if !waits(t, v, "hooks/x.sh") {
		t.Fatal("it still waits")
	}
}

func TestCaseCollidingPathsAreRefused(t *testing.T) {
	b := newMachine(t, "gpu-box")
	bn := Bundle{Store: "notes", From: "laptop", Files: map[string]FileState{}, Data: map[string][]byte{}}
	for _, n := range []string{"notes/a.md", "Notes/A.md"} {
		bn.Files[n] = file(n, "laptop", b.now)
		bn.Data[n] = []byte(n)
	}
	if r := push(t, b, bn); len(r.Applied) != 1 || len(r.Refused) != 1 || !strings.Contains(r.Refused[0].Reason, "only by case") {
		t.Fatalf("paths that differ only by case: %+v", r)
	}
}

// a guard: the review found no hole here, and it stays that way
func TestReceiveStaysInsideTheStoreFolder(t *testing.T) {
	b := newMachine(t, "gpu-box")
	outside := t.TempDir()
	must(t, os.MkdirAll(b.s.Dir, 0o755))
	must(t, os.Symlink(outside, filepath.Join(b.s.Dir, "skills")))
	must(t, os.WriteFile(filepath.Join(outside, "victim.md"), []byte("victim\n"), 0o644))
	must(t, os.Symlink(filepath.Join(outside, "victim.md"), filepath.Join(b.s.Dir, "leaf.md")))
	d := []byte("pwn\n")
	for _, rel := range []string{"../x.md", "/abs/x.md", "a/../../x.md", "a//b", "./a", "skills/x.md", "leaf.md", "a\\..\\b"} {
		var in, out bytes.Buffer
		tw := tar.NewWriter(&in)
		meta, _ := json.Marshal(Bundle{V: 1, Store: "notes", From: "laptop", Files: map[string]FileState{rel: file(string(d), "laptop", b.now)}})
		must(t, tw.WriteHeader(&tar.Header{Name: bundleMeta, Mode: 0o600, Size: int64(len(meta))}))
		_, err := tw.Write(meta)
		must(t, err)
		must(t, tw.WriteHeader(&tar.Header{Name: rel, Mode: 0o644, Size: int64(len(d)), Typeflag: tar.TypeReg}))
		_, err = tw.Write(d)
		must(t, err)
		must(t, tw.Close())
		_ = ServeRecv(b.s, &in, &out, "laptop")
	}
	es, _ := os.ReadDir(outside)
	if len(es) != 1 {
		t.Fatalf("nothing may be written outside the store: %v", es)
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "victim.md")); string(got) != "victim\n" {
		t.Fatalf("victim changed: %q", got)
	}
}
