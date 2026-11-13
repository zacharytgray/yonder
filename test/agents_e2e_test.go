package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentsStoreLocal: import, link, turning the layer off and on, and the
// review and secret refusals, on one machine.
func TestAgentsStoreLocal(t *testing.T) {
	f := newFleet(t, "testdata/fleet.json")
	f.must("init", "--yes", "--no-skill")
	home := filepath.Join(f.home, "yonder", "home") // ~ under YONDER_HOME
	src := t.TempDir()
	write := func(p, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, "CLAUDE.md"), "be brief\n", 0o644)
	write(filepath.Join(src, "skills/review/SKILL.md"), "review skill\n", 0o644)
	write(filepath.Join(src, "hooks/guard.sh"), "#!/bin/sh\n", 0o755)
	write(filepath.Join(src, "agents/.env"), "X=1\n", 0o644)
	write(filepath.Join(src, "settings.json"), "{}", 0o644)

	if r := f.run("", "agents", "import", src); r.code != 1 || !strings.Contains(r.stderr, "yonder enable agents") {
		t.Fatalf("import with the layer off: %+v", r)
	}
	f.must("enable", "agents")
	r := f.must("agents", "import", src)
	if !strings.Contains(r.stdout, "added instructions.md") || !strings.Contains(r.stdout, "added skills/review/SKILL.md") ||
		!strings.Contains(r.stdout, "added hooks/guard.sh") || strings.Contains(r.stdout, "settings.json") {
		t.Fatalf("import: %s", r.stdout)
	}
	store := filepath.Join(f.home, "yonder", "data", "agents")
	if b, _ := os.ReadFile(filepath.Join(store, "instructions.md")); string(b) != "be brief\n" {
		t.Fatalf("store: %q", b)
	}
	// a secret-looking file put in by hand is a FAIL, in sync and doctor
	write(filepath.Join(store, "skills/review/notes.md"), "key: -----BEGIN PRIVATE KEY-----\n", 0o644)
	if r := f.run("", "sync"); r.code != 1 || !strings.Contains(r.stdout, "agents: FAIL skills/review/notes.md: looks like a private key (line 1)") {
		t.Fatalf("sync refusal: %+v", r)
	}
	if r := f.run("", "doctor"); r.code != 1 || !strings.Contains(r.stdout, "skills/review/notes.md") {
		t.Fatalf("doctor refusal: %+v", r)
	}
	f.must("agents", "allow", "skills/review/notes.md")
	f.must("sync")

	// link into claude code, keeping the user's own text
	write(filepath.Join(home, ".claude/CLAUDE.md"), "my rules\n", 0o644)
	r = f.must("agents", "link", "--harness", "claude-code")
	if !strings.Contains(r.stdout, "wrote the yonder block in ~/.claude/CLAUDE.md") || !strings.Contains(r.stdout, "linked ~/.claude/skills/review") {
		t.Fatalf("link: %s", r.stdout)
	}
	claude, _ := os.ReadFile(filepath.Join(home, ".claude/CLAUDE.md"))
	if !strings.HasPrefix(string(claude), "my rules\n") || !strings.Contains(string(claude), "\nbe brief\n<!-- yonder:end -->") {
		t.Fatalf("CLAUDE.md: %s", claude)
	}
	// an edit in the store is rendered in at the next sync
	write(filepath.Join(store, "instructions.md"), "be briefer\n", 0o644)
	if r := f.must("doctor", "--json"); !strings.Contains(r.stdout, "is behind the store") {
		t.Fatalf("doctor should see the stale block: %s", r.stdout)
	}
	f.must("sync")
	if b, _ := os.ReadFile(filepath.Join(home, ".claude/CLAUDE.md")); !strings.Contains(string(b), "\nbe briefer\n<!-- yonder:end -->") {
		t.Fatalf("sync should re-render: %s", b)
	}
	write(filepath.Join(store, "instructions.md"), "be brief\n", 0o644)
	f.must("sync")
	if l, _ := os.Readlink(filepath.Join(home, ".claude/hooks/guard.sh")); l != filepath.Join(store, "hooks/guard.sh") {
		t.Fatalf("hook link: %q", l)
	}
	var st struct {
		Store struct {
			LayerOn bool     `json:"layer_on"`
			Files   int      `json:"files"`
			Linked  []string `json:"linked"`
			Review  string   `json:"review"`
		} `json:"store"`
	}
	if err := json.Unmarshal([]byte(f.must("agents", "status", "--json").stdout), &st); err != nil || !st.Store.LayerOn || st.Store.Files != 4 || len(st.Store.Linked) != 1 || st.Store.Review != "executables" {
		t.Fatalf("status: %+v %v", st, err)
	}

	// off: unlinked, the user's text back, the store kept
	r = f.must("disable", "agents")
	if !strings.Contains(r.stdout, "took the yonder block out of ~/.claude/CLAUDE.md") {
		t.Fatalf("disable: %s", r.stdout)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude/CLAUDE.md")); string(b) != "my rules\n" {
		t.Fatalf("CLAUDE.md after disable: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude/skills/review")); err == nil {
		t.Fatal("disable should unlink the skills")
	}
	if _, err := os.Stat(filepath.Join(store, "instructions.md")); err != nil {
		t.Fatal("disable keeps the store")
	}
	if r := f.run("", "sync", "agents"); r.code != 1 || !strings.Contains(r.stderr, "yonder enable agents") {
		t.Fatalf("sync agents while off: %+v", r)
	}
	// on again: linked again
	r = f.must("enable", "agents")
	if !strings.Contains(r.stdout, "linked ~/.claude/skills/review") {
		t.Fatalf("enable should relink: %s", r.stdout)
	}

	// history, log, diff, restore
	write(filepath.Join(store, "instructions.md"), "be brief. really.\n", 0o644)
	f.must("sync")
	f.must("agents", "import", filepath.Join(src, "CLAUDE.md"), "--replace")
	r = f.must("agents", "log", "instructions.md")
	if strings.Count(r.stdout, "instructions.md") < 3 || !strings.Contains(r.stdout, "by laptop") {
		t.Fatalf("log: %s", r.stdout)
	}
	if r := f.must("agents", "diff", "instructions.md"); !strings.Contains(r.stdout, "-be brief. really.") || !strings.Contains(r.stdout, "+be brief") {
		t.Fatalf("diff: %s", r.stdout)
	}
	f.must("agents", "restore", "instructions.md")
	if b, _ := os.ReadFile(filepath.Join(store, "instructions.md")); string(b) != "be brief. really.\n" {
		t.Fatalf("restore: %q", b)
	}
	if r := f.must("agents", "accept"); !strings.Contains(r.stdout, "nothing waits for review") {
		t.Fatalf("accept: %s", r.stdout)
	}

	f.must("agents", "unlink")
	if b, _ := os.ReadFile(filepath.Join(home, ".claude/CLAUDE.md")); string(b) != "my rules\n" {
		t.Fatalf("CLAUDE.md after unlink: %q", b)
	}
}
