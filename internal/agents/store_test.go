package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	refused := map[string]string{
		".env":                                    "x",
		"files/codex/auth.json":                   "{}",
		"skills/a/deploy.pem":                     "x",
		"hooks/id_ed25519":                        "x",
		"skills/a/my-token.txt":                   "x",
		"skills/yonder/SKILL.md":                  "x",
		"machines/gpu-box/skills/synced/SKILL.md": "x",
		"skills/a/notes.md":                       "line\n-----BEGIN OPENSSH PRIVATE KEY-----\n",
		"instructions.md":                         "use sk-ant-REDACTEDREDACTEDREDACTED1234 here",
		"hooks/x.sh":                              "export GH=ghp_" + strings.Repeat("a1B2", 9),
		"instructions.codex.md":                   "token: Zx9Qm2Lr7Tv4Wn8Ys3Kp6Hd1Fb5Gc0Ja",
	}
	for rel, content := range refused {
		if Check(rel, []byte(content)) == nil {
			t.Errorf("%s should be refused", rel)
		}
	}
	fine := map[string]string{
		"instructions.md":        "be brief. use `yonder run gpu-box --secret API_KEY` for keys.\n",
		"skills/a/SKILL.md":      "hash: " + strings.Repeat("ab12cd34", 8) + "\n", // hex hash
		"files/codex/hooks.json": `{"key": "op://Agents/api/credential"}`,
		"skills/a/long.md":       "AnExtraordinarilyLongIdentifierNameWithoutAnyDigitsAtAll\n",
	}
	for rel, content := range fine {
		if r := Check(rel, []byte(content)); r != nil {
			t.Errorf("%s should pass: %v", rel, r)
		}
	}
	r := Check("instructions.md", []byte("one\ntwo\n-----BEGIN RSA PRIVATE KEY-----\n"))
	if r == nil || !strings.Contains(r.Reason, "line 3") {
		t.Errorf("line number: %+v", r)
	}
}

func TestRuns(t *testing.T) {
	for rel, want := range map[string]bool{
		"hooks/guard.sh":                  true,
		"machines/gpu-box/hooks/x":        true,
		"files/codex/hooks.json":          true,
		"skills/a/SKILL.md":               false,
		"instructions.md":                 false,
		"files/codex/models.catalog.json": false,
	} {
		if Runs(rel, false) != want {
			t.Errorf("Runs(%s) != %v", rel, want)
		}
	}
	if !Runs("skills/a/run.sh", true) {
		t.Error("an executable file runs code")
	}
}

type fixture struct {
	t                     *testing.T
	home, storeDir, state string
}

func newFixture(t *testing.T) *fixture {
	root := t.TempDir()
	f := &fixture{t: t, home: filepath.Join(root, "home"), storeDir: filepath.Join(root, "data", "agents"), state: filepath.Join(root, "state", "agents")}
	for _, d := range []string{f.home, f.storeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fixture) put(dir, rel, content string) {
	f.t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) linker() *Linker {
	return &Linker{Home: f.home, StoreDir: f.storeDir, StateDir: f.state, Self: "laptop"}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func read(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestLinkAndUnlink(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, "instructions.md", "shared rules\n")
	f.put(f.storeDir, "instructions.claude.md", "claude extra\n")
	f.put(f.storeDir, "instructions.codex.md", "codex extra\n")
	f.put(f.storeDir, "skills/foo/SKILL.md", "foo\n")
	f.put(f.storeDir, "skills/bar/SKILL.md", "bar\n")
	f.put(f.storeDir, "claude/agents/reviewer.md", "a\n")
	f.put(f.storeDir, "hooks/guard.sh", "#!/bin/sh\n")
	f.put(f.storeDir, "hooks/guard.conflict-gpu-box-20261112.sh", "#!/bin/sh\n")
	f.put(f.storeDir, "files/codex/hooks.json", "{}\n")
	f.put(f.storeDir, "machines/laptop/skills/local/SKILL.md", "only here\n")
	f.put(f.storeDir, "machines/gpu-box/skills/gpu/SKILL.md", "not here\n")
	// what the user already has
	f.put(f.home, ".claude/CLAUDE.md", "my own rules\n")
	f.put(f.home, ".claude/skills/foo/SKILL.md", "my old foo\n")
	f.put(f.home, ".codex/hooks.json", `{"mine":true}`)

	l := f.linker()
	if err := l.Link([]string{"claude-code", "codex", "pi"}); err != nil {
		t.Fatal(err)
	}
	claude := read(filepath.Join(f.home, ".claude/CLAUDE.md"))
	if !strings.HasPrefix(claude, "my own rules\n\n"+blockBegin) || !strings.Contains(claude, "@"+filepath.Join(f.storeDir, "instructions.md")) ||
		!strings.Contains(claude, "instructions.claude.md") || strings.Contains(claude, "instructions.codex.md") {
		t.Fatalf("CLAUDE.md:\n%s", claude)
	}
	codex := read(filepath.Join(f.home, ".codex/AGENTS.md"))
	if !strings.HasPrefix(codex, renderMark) || !strings.Contains(codex, "shared rules") || !strings.Contains(codex, "codex extra") || strings.Contains(codex, "claude extra") {
		t.Fatalf("codex AGENTS.md:\n%s", codex)
	}
	if pi := read(filepath.Join(f.home, ".pi/agent/AGENTS.md")); !strings.Contains(pi, "shared rules") || strings.Contains(pi, "codex extra") {
		t.Fatalf("pi AGENTS.md:\n%s", pi)
	}
	for link, target := range map[string]string{
		".claude/skills/foo":         "skills/foo",
		".claude/skills/local":       "machines/laptop/skills/local",
		".agents/skills/bar":         "skills/bar",
		".claude/agents/reviewer.md": "claude/agents/reviewer.md",
		".claude/hooks/guard.sh":     "hooks/guard.sh",
		".codex/hooks.json":          "files/codex/hooks.json",
	} {
		got, err := os.Readlink(filepath.Join(f.home, link))
		if err != nil || got != filepath.Join(f.storeDir, target) {
			t.Errorf("%s -> %q (%v), want the store's %s", link, got, err, target)
		}
	}
	for _, p := range []string{".claude/skills/gpu", ".claude/hooks/guard.conflict-gpu-box-20261112.sh"} {
		if _, err := os.Lstat(filepath.Join(f.home, p)); err == nil {
			t.Errorf("%s should not be linked", p)
		}
	}
	if len(l.Problems()) != 0 {
		t.Fatalf("problems: %v", l.Problems())
	}

	// again: nothing changes, and removing a skill drops its link
	if err := l.Link(nil); err != nil {
		t.Fatal(err)
	}
	if got := read(filepath.Join(f.home, ".claude/CLAUDE.md")); got != claude {
		t.Fatalf("second link changed CLAUDE.md:\n%s", got)
	}
	os.RemoveAll(filepath.Join(f.storeDir, "skills/bar"))
	if err := l.Link(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.home, ".agents/skills/bar")); err == nil {
		t.Fatal("a skill gone from the store should lose its link")
	}

	// unlink puts everything back
	if err := l.Unlink(false); err != nil {
		t.Fatal(err)
	}
	if got := read(filepath.Join(f.home, ".claude/CLAUDE.md")); got != "my own rules\n" {
		t.Fatalf("CLAUDE.md after unlink: %q", got)
	}
	if got := read(filepath.Join(f.home, ".claude/skills/foo/SKILL.md")); got != "my old foo\n" {
		t.Fatalf("the old foo skill should be back: %q", got)
	}
	if got := read(filepath.Join(f.home, ".codex/hooks.json")); got != `{"mine":true}` {
		t.Fatalf("hooks.json should be back: %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".codex/AGENTS.md")); err == nil {
		t.Fatal("the rendered AGENTS.md should be gone")
	}
	if l.Linked() {
		t.Fatal("unlinked")
	}
}

func TestLinkRefusesSymlinksUnlessReplace(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, "instructions.md", "shared\n")
	f.put(f.storeDir, "skills/foo/SKILL.md", "foo\n")
	elsewhere := filepath.Join(filepath.Dir(f.home), "dotfiles")
	f.put(elsewhere, "CLAUDE.md", "dotfiles text\n")
	f.put(elsewhere, "skills/old/SKILL.md", "old\n")
	must(t, os.MkdirAll(filepath.Join(f.home, ".claude"), 0o755))
	must(t, os.Symlink(filepath.Join(elsewhere, "CLAUDE.md"), filepath.Join(f.home, ".claude/CLAUDE.md")))
	must(t, os.Symlink(filepath.Join(elsewhere, "skills"), filepath.Join(f.home, ".claude/skills")))

	l := f.linker()
	err := l.Link([]string{"claude-code"})
	if err == nil || !strings.Contains(err.Error(), "--replace-links") {
		t.Fatalf("symlinks should be refused: %v", err)
	}
	if read(filepath.Join(elsewhere, "CLAUDE.md")) != "dotfiles text\n" {
		t.Fatal("nothing may be written through the link")
	}
	if _, err := os.Lstat(filepath.Join(elsewhere, "skills/foo")); err == nil {
		t.Fatal("no link may land in the linked folder")
	}

	l.Replace = true
	if err := l.Link([]string{"claude-code"}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(filepath.Join(f.home, ".claude/skills")); st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the skills folder should be real now")
	}
	if !strings.Contains(read(filepath.Join(f.home, ".claude/CLAUDE.md")), blockBegin) {
		t.Fatal("CLAUDE.md should hold the block")
	}
	if _, err := os.Stat(filepath.Join(f.home, ".claude/skills/yonder/SKILL.md")); err != nil {
		t.Fatal("the yonder skill should be reinstalled in the new folder")
	}
	l.Replace = false
	if err := l.Unlink(false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(filepath.Join(f.home, ".claude/CLAUDE.md")); got != filepath.Join(elsewhere, "CLAUDE.md") {
		t.Fatalf("CLAUDE.md link should be back: %q", got)
	}
	if got, _ := os.Readlink(filepath.Join(f.home, ".claude/skills")); got != filepath.Join(elsewhere, "skills") {
		t.Fatalf("the skills folder link should be back: %q", got)
	}
	if read(filepath.Join(elsewhere, "skills/old/SKILL.md")) != "old\n" {
		t.Fatal("the linked folder's own files are untouched")
	}
}

func TestUnlinkPutsBackReplacedSkillsFolder(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, "skills/foo/SKILL.md", "foo\n")
	elsewhere := filepath.Join(filepath.Dir(f.home), "old-skills")
	must(t, os.MkdirAll(elsewhere, 0o755))
	must(t, os.MkdirAll(filepath.Join(f.home, ".agents"), 0o755))
	must(t, os.Symlink(elsewhere, filepath.Join(f.home, ".agents/skills")))
	l := f.linker()
	l.Replace = true
	if err := l.Link([]string{"codex"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Unlink(false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(filepath.Join(f.home, ".agents/skills")); got != elsewhere {
		t.Fatalf("the skills folder link should be back: %q", got)
	}
}

func TestPauseAndResume(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, "skills/foo/SKILL.md", "foo\n")
	l := f.linker()
	if err := l.Link([]string{"claude-code"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Unlink(true); err != nil {
		t.Fatal(err)
	}
	st, _ := l.State()
	if !st.Paused || len(st.Harnesses) != 1 || len(st.Entries) != 0 {
		t.Fatalf("paused state: %+v", st)
	}
	if _, err := os.Lstat(filepath.Join(f.home, ".claude/skills/foo")); err == nil {
		t.Fatal("paused means unlinked")
	}
	if l.Linked() {
		t.Fatal("paused is not linked")
	}
}

func TestPlanImport(t *testing.T) {
	src := t.TempDir()
	put := func(rel, content string, mode os.FileMode) {
		p := filepath.Join(src, rel)
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte(content), mode))
	}
	put("CLAUDE.md", "rules\n", 0o644)
	put("settings.json", "{}", 0o644)
	put("skills/foo/SKILL.md", "foo\n", 0o644)
	put("skills/foo/run.sh", "#!/bin/sh\n", 0o755)
	put("skills/yonder/SKILL.md", "x", 0o644)
	put("skills/notaskill/readme.md", "x", 0o644)
	put("agents/reviewer.md", "a\n", 0o644)
	put("hooks/guard.sh", "#!/bin/sh\n", 0o755)
	p, err := PlanImport(ImportOptions{Src: src})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range p.Files {
		got[f.Rel] = f.Exec
	}
	for rel, exec := range map[string]bool{
		"instructions.md": false, "skills/foo/SKILL.md": false, "skills/foo/run.sh": true,
		"claude/agents/reviewer.md": false, "hooks/guard.sh": true,
	} {
		if e, ok := got[rel]; !ok || e != exec {
			t.Errorf("%s: in=%v exec=%v", rel, ok, e)
		}
	}
	if len(got) != 5 || len(p.Skipped) != 2 {
		t.Fatalf("files %v skipped %v", got, p.Skipped)
	}

	p, err = PlanImport(ImportOptions{Src: filepath.Join(src, "CLAUDE.md"), Harness: "claude", Machine: "gpu-box"})
	if err != nil || len(p.Files) != 1 || p.Files[0].Rel != "machines/gpu-box/instructions.claude.md" {
		t.Fatalf("overlay import: %+v %v", p, err)
	}
	if _, err := PlanImport(ImportOptions{Src: filepath.Join(src, "settings.json")}); err == nil {
		t.Fatal("a file of no known kind needs --kind")
	}
	if _, err := PlanImport(ImportOptions{Src: filepath.Join(src, "settings.json"), Kinds: []string{KindFiles}}); err == nil {
		t.Fatal("files need --harness")
	}
}

func TestBlockRoundTrip(t *testing.T) {
	for _, text := range []string{"", "mine\n", "mine", "top\n\nmore\n"} {
		with := withBlock(text, "@x\n")
		if again := withBlock(with, "@y\n"); strings.Count(again, blockBegin) != 1 || !strings.Contains(again, "@y") {
			t.Errorf("replace block in %q: %q", text, again)
		}
		back := withoutBlock(with)
		want := text
		if want != "" && !strings.HasSuffix(want, "\n") {
			want += "\n"
		}
		if back != want {
			t.Errorf("round trip %q: got %q", text, back)
		}
	}
}

func TestDiff(t *testing.T) {
	d := Diff([]byte("a\nb\nc\n"), []byte("a\nB\nc\n"), "old", "new")
	if !strings.Contains(d, "-b\n+B\n") || !strings.HasPrefix(d, "--- old\n+++ new\n") {
		t.Fatalf("diff:\n%s", d)
	}
	if Diff([]byte("same"), []byte("same"), "a", "b") != "" {
		t.Fatal("no diff for equal input")
	}
}
