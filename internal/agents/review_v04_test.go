package agents

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// review item 12: a trim keeps the rest of the file as it was
func TestTrimKeepsTheRestByteForByte(t *testing.T) {
	src := "intro\n\n\n```python\ndef a():\n    pass\n\n\ndef b():\n    pass\n```\n\n## Old\nbody\n\n## Keep\nkept\n"
	got, removed, err := TrimSections(src, []string{"## old"})
	if err != nil || len(removed) != 1 {
		t.Fatalf("removed %v, %v", removed, err)
	}
	want := "intro\n\n\n```python\ndef a():\n    pass\n\n\ndef b():\n    pass\n```\n\n## Keep\nkept\n"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

// review item 5: a # line inside a code fence is code, not a heading
func TestFencedHashIsNotAHeading(t *testing.T) {
	src := "## Build\nRun this:\n```bash\n# install deps\nmake deps\n# run tests\nmake test\n```\nThen push.\n\n## Style\nbe brief\n"
	var keys []string
	for _, s := range splitSections(src) {
		keys = append(keys, s.key)
	}
	if strings.Join(keys, "|") != "## build|## style" {
		t.Fatalf("keys: %q", keys)
	}
	r := Compare([]MachineFiles{
		{Machine: "laptop", Files: map[string][]byte{".claude/CLAUDE.md": []byte(src)}},
		{Machine: "gpu-box", Files: map[string][]byte{".claude/CLAUDE.md": []byte("## Style\nbe brief\n")}},
	})
	rc, err := BuildReconcile(r, Plan{V: 1, Sections: []PlanSection{{Key: "## build", Action: ActShared, From: "laptop"}}}, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if got := rc.Store["instructions.md"]; !strings.Contains(got, "# run tests\nmake test\n```\nThen push.") {
		t.Fatalf("the whole fenced block should move: %q", got)
	}
	left, _, _ := TrimSections(src, rc.Trims["laptop"][0].Remove)
	if left != "## Style\nbe brief\n" {
		t.Fatalf("left on laptop: %q", left)
	}
}

// review item 3: a trim waits for store text the plan didn't change, too
func TestTrimNeedsStoreTextItDidntChange(t *testing.T) {
	r := Compare([]MachineFiles{
		{Machine: "laptop", Files: map[string][]byte{".claude/CLAUDE.md": []byte("## Style\nbe brief\n")}},
		{Machine: "gpu-box", Files: map[string][]byte{".claude/CLAUDE.md": []byte("## Other\nx\n")}},
	})
	cur := "## Style\nbe brief\n"
	rc, err := BuildReconcile(r, Plan{V: 1, Sections: []PlanSection{{Key: "## style", Action: ActShared, From: "laptop"}}},
		func(rel string) (string, bool) { return cur, rel == "instructions.md" })
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Store) != 0 || len(rc.Trims["laptop"]) != 1 {
		t.Fatalf("store %v trims %v", rc.Store, rc.Trims)
	}
	if rc.Needs["laptop"]["instructions.md"] != cur {
		t.Fatalf("the trim must wait for instructions.md: %v", rc.Needs)
	}
}

// review item 2: a re-rendered block doesn't make the user's text look edited,
// and whitespace in their own text still counts
func TestOwnHashIgnoresBlockRerender(t *testing.T) {
	own := "## Style\nbe brief\n\n    indented code\n"
	a := own + "\n" + blockBegin + "\nbriefing one\n" + blockEnd + "\n"
	b := own + "\n" + blockBegin + "\nbriefing two, re-rendered\n" + blockEnd + "\n"
	if OwnHash([]byte(a)) != OwnHash([]byte(b)) {
		t.Fatal("a re-rendered block changed the own-text hash")
	}
	if OwnHash([]byte(own)) != OwnHash([]byte(a)) {
		t.Fatal("adding the block changed the own-text hash")
	}
	if OwnHash([]byte(strings.Replace(own, "    indented", "  indented", 1))) == OwnHash([]byte(own)) {
		t.Fatal("whitespace in the user's own text must count")
	}
	fp := func(data string) string {
		return Compare([]MachineFiles{{Machine: "laptop", Files: map[string][]byte{".claude/CLAUDE.md": []byte(data)}}}).Fingerprint
	}
	if fp(a) != fp(b) {
		t.Fatal("a re-render made the plan fingerprint stale")
	}
}

// review item 10: Codex's home follows CODEX_HOME, and an override file is seen
func TestCodexHomeAndOverride(t *testing.T) {
	yh := t.TempDir()
	t.Setenv("YONDER_HOME", yh)
	home := filepath.Join(yh, "home")
	t.Setenv("CODEX_HOME", "")
	if got := InstructionsPath(home, ".codex/AGENTS.md"); got != filepath.Join(home, ".codex", "AGENTS.md") {
		t.Fatalf("default: %s", got)
	}
	ch := filepath.Join(home, "codex-elsewhere")
	t.Setenv("CODEX_HOME", ch)
	if got := InstructionsPath(home, ".codex/AGENTS.md"); got != filepath.Join(ch, "AGENTS.md") {
		t.Fatalf("CODEX_HOME: %s", got)
	}
	// under YONDER_HOME a CODEX_HOME outside it never counts
	t.Setenv("CODEX_HOME", t.TempDir())
	if got := InstructionsPath(home, ".codex/AGENTS.md"); got != filepath.Join(home, ".codex", "AGENTS.md") {
		t.Fatalf("outside YONDER_HOME: %s", got)
	}
	t.Setenv("CODEX_HOME", ch)
	if _, ok := CodexOverride(home); ok {
		t.Fatal("no override yet")
	}
	if err := os.MkdirAll(ch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ch, "AGENTS.override.md"), []byte("my override\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := CodexOverride(home); !ok || !strings.Contains(CodexOverrideNote(home), "never writes into it") {
		t.Fatal("override not seen")
	}
}

// recheck leftover 1: once linked, Codex's folder doesn't move with the
// environment of a later sync (the timer, a peer's ssh, another shell)
func TestCodexHomeSticksAcrossRelink(t *testing.T) {
	yh := t.TempDir()
	t.Setenv("YONDER_HOME", yh)
	home := filepath.Join(yh, "home")
	storeDir := filepath.Join(yh, "data", StoreName)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "instructions.md"), []byte("## Style\nKEEP_RULE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(home, "custom-codex")
	t.Setenv("CODEX_HOME", custom)
	if err := NewLinker("laptop", io.Discard).Link([]string{"codex"}); err != nil {
		t.Fatal(err)
	}
	inCustom := filepath.Join(custom, "AGENTS.md")
	if !strings.Contains(read(inCustom), "KEEP_RULE") {
		t.Fatalf("not rendered in CODEX_HOME: %q", read(inCustom))
	}
	for _, env := range []string{"", filepath.Join(home, "other-codex")} {
		t.Setenv("CODEX_HOME", env)
		if err := NewLinker("laptop", io.Discard).Link(nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(read(inCustom), "KEEP_RULE") {
			t.Fatalf("CODEX_HOME=%q: the relink moved Codex's file away: %q", env, read(inCustom))
		}
		if _, err := os.Stat(filepath.Join(home, ".codex", "AGENTS.md")); err == nil {
			t.Fatalf("CODEX_HOME=%q: rendered into ~/.codex", env)
		}
		if got := CodexHome(home); got != custom {
			t.Fatalf("CodexHome = %s, want the linked %s", got, custom)
		}
	}
}
