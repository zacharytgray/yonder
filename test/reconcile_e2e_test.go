package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReconcileLocal: a plan applied on one machine moves a section into the
// store, drops one, leaves one, links the store in, and undo puts it all back.
func TestReconcileLocal(t *testing.T) {
	f := newFleet(t, "testdata/fleet.json")
	f.env = append(f.env, "YONDER_SCHEDULE_JITTER=off")
	f.must("init", "--yes", "--no-skill", "--layers", "agents")
	home := filepath.Join(f.home, "yonder", "home")
	claude := filepath.Join(home, ".claude", "CLAUDE.md")
	orig := "# Style\nbe brief\n\n# Scratch\ntodo\n\n# Mine\nkeep this here\n"
	if err := os.MkdirAll(filepath.Dir(claude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claude, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	var g struct {
		Fingerprint string `json:"fingerprint"`
		Sections    []struct {
			Key string `json:"key"`
		} `json:"sections"`
	}
	if err := json.Unmarshal([]byte(f.must("agents", "gather", "--json").stdout), &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Sections) != 3 || g.Fingerprint == "" {
		t.Fatalf("gather: %+v", g)
	}
	plan := filepath.Join(f.home, "plan.json")
	write := func(body string) {
		if err := os.WriteFile(plan, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"v":1,"fingerprint":"` + g.Fingerprint + `","sections":[
	  {"key":"# style","action":"shared","from":"laptop"},
	  {"key":"# scratch","action":"drop"},
	  {"key":"# mine","action":"leave"}]}`)
	r := f.must("agents", "reconcile", "--plan", plan, "--dry-run")
	if !strings.Contains(r.stdout, "+# Style") || !strings.Contains(r.stdout, "take out # style, # scratch") || !strings.Contains(r.stdout, "nothing written") {
		t.Fatalf("dry run: %s", r.stdout)
	}
	if b, _ := os.ReadFile(claude); string(b) != orig {
		t.Fatal("dry run wrote")
	}
	r = f.must("agents", "reconcile", "--plan", plan)
	store := filepath.Join(f.home, "yonder", "data", "agents", "instructions.md")
	if b, _ := os.ReadFile(store); string(b) != "# Style\nbe brief\n" {
		t.Fatalf("store: %q\n%s", b, r.stdout)
	}
	b, _ := os.ReadFile(claude)
	got := string(b)
	own, block, found := strings.Cut(got, "<!-- yonder:begin")
	if !found {
		t.Fatalf("no block after reconcile:\n%s", got)
	}
	if !strings.Contains(own, "keep this here") || strings.Contains(own, "be brief") || strings.Contains(got, "todo") || !strings.Contains(block, "be brief") {
		t.Fatalf("CLAUDE.md after reconcile:\n%s\n%s", got, r.stdout)
	}
	// a stale plan is refused
	if r := f.run("", "agents", "reconcile", "--plan", plan); r.code == 0 || !strings.Contains(r.stderr, "changed since the plan was made") {
		t.Fatalf("stale plan: %+v", r)
	}
	r = f.must("agents", "reconcile", "--undo")
	if !strings.Contains(r.stdout, "undone") {
		t.Fatalf("undo: %s", r.stdout)
	}
	b, _ = os.ReadFile(claude)
	if !strings.HasPrefix(string(b), orig) {
		t.Fatalf("undo didn't put the text back:\n%s", b)
	}
	if _, err := os.Stat(store); err == nil {
		t.Fatal("undo left instructions.md in the store")
	}
	if r := f.run("", "agents", "reconcile", "--undo"); r.code == 0 {
		t.Fatal("a second undo should say there is nothing to undo")
	}
}

// recheck leftover 2: a plan whose text won't render writes nothing, and a
// run where a machine refuses says so in its last line
func TestReconcileRefusesBeforeWriting(t *testing.T) {
	f := newFleet(t, "testdata/fleet.json")
	f.must("init", "--yes", "--no-skill", "--layers", "agents", "--no-schedule")
	home := filepath.Join(f.home, "yonder", "home")
	put := func(rel, body string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(".claude/CLAUDE.md", "# Style\nbe brief\n")
	put(".codex/AGENTS.md", "# Style\nbe brief\n")
	f.must("agents", "link", "--harness", "claude-code")
	plan := filepath.Join(f.home, "plan.json")
	store := filepath.Join(f.home, "yonder", "data", "agents", "instructions.md")

	if err := os.WriteFile(plan, []byte(`{"v":1,"sections":[{"key":"# style","action":"merge","text":"# Style\nuse {{model:fast}}"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := f.run("", "agents", "reconcile", "--plan", plan)
	if r.code == 0 || !strings.Contains(r.stderr, "wouldn't render") {
		t.Fatalf("unrenderable plan: %+v", r)
	}
	if _, err := os.Stat(store); err == nil {
		t.Fatal("the store got text that can't render")
	}

	// codex isn't linked here: its file stays, and the last line says not applied
	if err := os.WriteFile(plan, []byte(`{"v":1,"sections":[{"key":"# style","action":"shared","from":"laptop:~/.claude/CLAUDE.md"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r = f.run("", "agents", "reconcile", "--plan", plan)
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if r.code == 0 || !strings.Contains(lines[len(lines)-1], "NOT fully applied") {
		t.Fatalf("last line: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".codex/AGENTS.md")); string(b) != "# Style\nbe brief\n" {
		t.Fatalf("codex's file changed: %q", b)
	}
}
