package agents

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/store"
)

// receiver is a machine named gpu-box with its own home and the real agents
// store, taking bundles from laptop.
func receiver(t *testing.T) (home string, s *store.Store) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "realhome"))
	t.Setenv("YONDER_HOME", filepath.Join(root, "yh"))
	home = Home()
	if !strings.HasPrefix(home, root) {
		t.Fatalf("home not isolated: %s", home)
	}
	must(t, os.MkdirAll(home, 0o755))
	s, err := store.Open(StoreName, "gpu-box")
	must(t, err)
	return home, s
}

func receive(t *testing.T, s *store.Store, files map[string]string) store.PushResult {
	t.Helper()
	b := store.Bundle{Store: StoreName, From: "laptop", Files: map[string]store.FileState{}, Data: map[string][]byte{}}
	for rel, c := range files {
		b.Files[rel] = store.FileState{Hash: hashText(c), Size: int64(len(c)), Version: store.Vector{"laptop": 1},
			Origin: store.Origin{Machine: "laptop", Time: time.Now().UTC()}}
		b.Data[rel] = []byte(c)
	}
	var in, out bytes.Buffer
	must(t, store.WriteBundle(&in, b))
	must(t, store.ServeRecv(s, &in, &out, "laptop"))
	var r store.PushResult
	must(t, json.Unmarshal(out.Bytes(), &r))
	return r
}

func refusedPaths(r store.PushResult) string {
	var ps []string
	for _, x := range r.Refused {
		ps = append(ps, x.Path)
	}
	return strings.Join(ps, ",")
}

func TestSettingsFilesNeverSyncOrLink(t *testing.T) {
	home, s := receiver(t)
	put := func(p, c string) {
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte(c), 0o644))
	}
	put(filepath.Join(home, ".claude/settings.json"), `{"mine":true}`)
	put(filepath.Join(home, ".codex/config.toml"), "mine = true\n")
	put(filepath.Join(s.Dir, "skills/x/SKILL.md"), "x\n")
	must(t, NewLinker("gpu-box", nil).Link([]string{"claude-code", "codex"}))

	r := receive(t, s, map[string]string{
		"files/claude/settings.json":       `{"hooks":{}}`,
		"files/claude/settings.local.json": `{}`,
		"files/codex/config.toml":          "notify = []\n",
		"files/codex/AGENTS.md":            "text\n",
		"files/codex/hooks.json":           "{}",
		"files/codex/models.catalog.json":  "{}",
	})
	if len(r.Refused) != 4 || len(r.Pending) != 2 || len(r.Applied) != 0 {
		t.Fatalf("settings refused, the rest waits for review: refused %s, %+v", refusedPaths(r), r)
	}
	if read(filepath.Join(home, ".claude/settings.json")) != `{"mine":true}` || read(filepath.Join(home, ".codex/config.toml")) != "mine = true\n" {
		t.Fatal("the user's settings must stay")
	}
	// a settings file put in the store by hand is never linked either
	put(filepath.Join(s.Dir, "files/claude/settings.json"), "{}")
	must(t, NewLinker("gpu-box", nil).Link(nil))
	if _, err := os.Readlink(filepath.Join(home, ".claude/settings.json")); err == nil {
		t.Fatal("settings.json must not be linked")
	}
}

func TestImportSkipsSettingsFiles(t *testing.T) {
	src := filepath.Join(t.TempDir(), ".claude")
	must(t, os.MkdirAll(src, 0o755))
	for _, n := range []string{"settings.json", ".credentials.json", "keybindings.json"} {
		must(t, os.WriteFile(filepath.Join(src, n), []byte("{}"), 0o644))
	}
	p, err := PlanImport(ImportOptions{Src: src, Kinds: []string{KindFiles}, Harness: "claude-code"})
	must(t, err)
	if len(p.Files) != 1 || p.Files[0].Rel != "files/claude/keybindings.json" || len(p.Skipped) != 1 {
		t.Fatalf("settings and dotfiles stay out: %+v", p)
	}
	if Check("files/claude/.credentials.json", nil) == nil {
		t.Fatal("credentials never sync")
	}
}

func TestCaseVariantsCannotSkipReview(t *testing.T) {
	for _, rel := range []string{"Hooks/evil.sh", "FILES/codex/hooks.json", "files/codex/Hooks.json", "machines/Gpu-Box/HOOKS/x"} {
		if !Runs(rel, false) {
			t.Errorf("%s runs code", rel)
		}
	}
	for _, rel := range []string{"skills/Yonder/SKILL.md", "SKILLS/SYNCED/x.md", "files/claude/Settings.json", "files/codex/CONFIG.TOML"} {
		if r := Check(rel, []byte("x")); r == nil || !r.Final {
			t.Errorf("%s should be refused for good: %v", rel, r)
		}
	}
	home, s := receiver(t)
	must(t, os.MkdirAll(filepath.Join(home, ".claude/hooks"), 0o755))
	r := receive(t, s, map[string]string{"Hooks/evil.sh": "#!/bin/sh\nevil\n", "skills/Yonder/SKILL.md": "fake\n"})
	if len(r.Pending) != 1 || len(r.Refused) != 1 {
		t.Fatalf("the hook waits, the reserved name is refused: %+v", r)
	}
	must(t, NewLinker("gpu-box", nil).Link([]string{"claude-code"}))
	if _, err := os.Lstat(filepath.Join(home, ".claude/hooks/evil.sh")); err == nil {
		t.Fatal("an unreviewed hook must not be linked")
	}
}

func TestSecretNamesCovered(t *testing.T) {
	for _, n := range []string{".credentials.json", ".git-credentials", ".netrc", "_netrc", "cert.p12", "cert.PFX"} {
		if r := Check("skills/x/"+n, nil); r == nil || !r.Final {
			t.Errorf("%s should never sync", n)
		}
	}
}

func TestLinkBacksUpFileLeftByRenameSave(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, "claude/commands/foo.md", "store foo\n")
	l := f.linker()
	must(t, l.Link([]string{"claude-code"}))
	p := filepath.Join(f.home, ".claude/commands/foo.md")
	// an editor saves by rename: the link becomes a real file with new text
	must(t, os.WriteFile(p+".tmp", []byte("my newer edits\n"), 0o644))
	must(t, os.Rename(p+".tmp", p))
	must(t, l.Link(nil))
	if got, _ := os.Readlink(p); got != filepath.Join(f.storeDir, "claude/commands/foo.md") {
		t.Fatalf("relinked: %q", got)
	}
	if !backedUp(t, f.state, "my newer edits\n") {
		t.Fatal("the edited file must be backed up, not deleted")
	}
}

func backedUp(t *testing.T, state, content string) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(filepath.Join(state, "backup"), func(p string, i os.FileInfo, err error) error {
		if err == nil && !i.IsDir() && read(p) == content {
			found = true
		}
		return nil
	})
	return found
}

func TestUserRewriteOfRenderedFileIsKept(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, "instructions.md", "shared rules\n")
	l := f.linker()
	must(t, l.Link([]string{"codex"}))
	p := filepath.Join(f.home, ".codex/AGENTS.md")
	must(t, os.WriteFile(p, []byte("my own agents file v1\n"), 0o644))
	must(t, l.Link(nil))
	got := read(p)
	if !strings.HasPrefix(got, "my own agents file v1\n") || !strings.Contains(got, blockBegin) {
		t.Fatalf("a file without the marker is the user's: it gets a block: %q", got)
	}
	if !backedUp(t, f.state, "my own agents file v1\n") {
		t.Fatal("backed up before the change")
	}
	must(t, os.WriteFile(p, []byte("my own agents file v2\n"), 0o644))
	must(t, l.Link(nil))
	if !backedUp(t, f.state, "my own agents file v2\n") {
		t.Fatal("backed up before every change")
	}
	must(t, os.WriteFile(p, []byte("all mine now\n"), 0o644))
	must(t, l.Unlink(true)) // as a peer's `disable agents` does
	if read(p) != "all mine now\n" {
		t.Fatalf("unlink never removes a file without the marker: %q", read(p))
	}
}

func TestBlockMarkers(t *testing.T) {
	// store text holding a marker is refused, and not rendered
	f := newFixture(t)
	f.put(f.home, ".claude/CLAUDE.md", "my own rules\n")
	f.put(f.storeDir, "instructions.md", "a line naming `"+blockEnd+"` in passing\n")
	if r := Check("instructions.md", []byte(read(filepath.Join(f.storeDir, "instructions.md")))); r == nil || !r.Final {
		t.Fatal("a marker in store text never syncs")
	}
	l := f.linker()
	must(t, l.Link([]string{"claude-code"}))
	must(t, l.Link(nil))
	if got := read(filepath.Join(f.home, ".claude/CLAUDE.md")); got != "my own rules\n" {
		t.Fatalf("nothing leaks past the block: %q", got)
	}

	// the last end marker closes the block
	text := "top\n\n" + blockBegin + "\nold\n" + blockEnd + "\nmine\n" + blockEnd + "\nbelow\n"
	next, err := withBlock(text, "new\n")
	must(t, err)
	if next != "top\n\n"+blockBegin+"\nnew\n"+blockEnd+"\nbelow\n" {
		t.Fatalf("last end marker: %q", next)
	}

	// a begin with no end: yonder changes nothing and says why
	f2 := newFixture(t)
	f2.put(f2.storeDir, "instructions.md", "rules\n")
	f2.put(f2.home, ".claude/CLAUDE.md", "top\n")
	l2 := f2.linker()
	must(t, l2.Link([]string{"claude-code"}))
	p := filepath.Join(f2.home, ".claude/CLAUDE.md")
	open := "top\n\n" + blockBegin + "\nUSER NOTES WRITTEN LATER\n"
	must(t, os.WriteFile(p, []byte(open), 0o644))
	if err := l2.Link(nil); err == nil || !strings.Contains(err.Error(), "no end marker") {
		t.Fatalf("an open block is refused: %v", err)
	}
	if read(p) != open {
		t.Fatal("the user's text must stay")
	}
	if ps := l2.Problems(); len(ps) == 0 || !strings.Contains(strings.Join(ps, "\n"), "no end marker") {
		t.Fatalf("doctor explains: %v", ps)
	}

	// importing a CLAUDE.md that holds yonder's block takes only the user's text
	src := filepath.Join(t.TempDir(), "CLAUDE.md")
	must(t, os.WriteFile(src, []byte("my rules\n\n"+blockBegin+"\nrendered\n"+blockEnd+"\n"), 0o644))
	plan, err := PlanImport(ImportOptions{Src: src})
	must(t, err)
	if len(plan.Files) != 1 || string(plan.Files[0].Data) != "my rules\n" {
		t.Fatalf("import strips the block: %+v", plan)
	}
}

func TestReplaceLinksCarriesTheUsersOwnFiles(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, "instructions.md", "shared\n")
	f.put(f.storeDir, "skills/foo/SKILL.md", "foo\n")
	elsewhere := filepath.Join(filepath.Dir(f.home), "dotfiles")
	f.put(elsewhere, "CLAUDE.md", "dotfiles text\n")
	f.put(elsewhere, "skills/mine/SKILL.md", "mine\n")
	must(t, os.MkdirAll(filepath.Join(f.home, ".claude"), 0o755))
	must(t, os.Symlink(filepath.Join(elsewhere, "CLAUDE.md"), filepath.Join(f.home, ".claude/CLAUDE.md")))
	must(t, os.Symlink(filepath.Join(elsewhere, "skills"), filepath.Join(f.home, ".claude/skills")))
	l := f.linker()
	l.Replace = true
	must(t, l.Link([]string{"claude-code"}))
	l.Replace = false
	claude := read(filepath.Join(f.home, ".claude/CLAUDE.md"))
	if !strings.HasPrefix(claude, "dotfiles text\n") || !strings.Contains(claude, "shared") {
		t.Fatalf("the user's text comes along: %q", claude)
	}
	if read(filepath.Join(f.home, ".claude/skills/mine/SKILL.md")) != "mine\n" {
		t.Fatal("the user's own skills stay visible")
	}
	must(t, l.Link(nil))
	if read(filepath.Join(f.home, ".claude/skills/mine/SKILL.md")) != "mine\n" {
		t.Fatal("a relink keeps them")
	}
	must(t, l.Unlink(false))
	if got, _ := os.Readlink(filepath.Join(f.home, ".claude/CLAUDE.md")); got != filepath.Join(elsewhere, "CLAUDE.md") {
		t.Fatalf("CLAUDE.md link back: %q", got)
	}
	if got, _ := os.Readlink(filepath.Join(f.home, ".claude/skills")); got != filepath.Join(elsewhere, "skills") {
		t.Fatalf("skills link back: %q", got)
	}
	if read(filepath.Join(elsewhere, "CLAUDE.md")) != "dotfiles text\n" {
		t.Fatal("the link target is untouched")
	}
}

func TestLinkWaitsForAnotherLink(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, "skills/foo/SKILL.md", "foo\n")
	must(t, os.MkdirAll(f.state, 0o700))
	held, err := os.OpenFile(filepath.Join(f.state, "links.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	must(t, err)
	defer held.Close()
	must(t, syscall.Flock(int(held.Fd()), syscall.LOCK_EX))
	old := linkLockWait
	linkLockWait = 100 * time.Millisecond
	defer func() { linkLockWait = old }()
	if err := f.linker().Link([]string{"claude-code"}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("a second link must wait for the first: %v", err)
	}
	must(t, syscall.Flock(int(held.Fd()), syscall.LOCK_UN))
	must(t, f.linker().Link([]string{"claude-code"}))
}

func TestDiffShowsModeAndEscapesControlCharacters(t *testing.T) {
	d := Diff([]byte("a\n"), []byte("a\x1b[2J\u202e\n"), "live", "incoming")
	if strings.ContainsAny(d, "\x1b\u202e") || !strings.Contains(d, "a\\x1b[2J\\u202e") {
		t.Fatalf("escaped: %q", d)
	}
	if ModeChange(false, true) != "mode: not executable -> executable\n" || ModeChange(true, true) != "" {
		t.Fatal("mode line")
	}
}
