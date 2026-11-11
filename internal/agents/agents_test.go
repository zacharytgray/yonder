package agents

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// a home with no harness binaries on PATH
func bare(t *testing.T) string {
	t.Setenv("PATH", t.TempDir())
	return t.TempDir()
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func byPath(rs []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range rs {
		if _, ok := m[r.Path]; !ok {
			m[r.Path] = r
		}
	}
	return m
}

func TestInstallDetectedOnly(t *testing.T) {
	home := bare(t)
	mkdir(t, filepath.Join(home, ".claude"))
	mkdir(t, filepath.Join(home, ".pi"))
	rs, err := Install(Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(rs)
	claude := filepath.Join(home, ".claude", "skills", "yonder")
	shared := filepath.Join(home, ".agents", "skills", "yonder")
	if got[claude].Action != "installed" || got[shared].Action != "installed" || strings.Join(got[shared].Names, ",") != "pi" {
		t.Fatalf("results: %+v", rs)
	}
	if r := got[filepath.Join(home, ".hermes", "skills", "yonder")]; r.Action != "skipped" {
		t.Fatalf("hermes should be skipped: %+v", r)
	}
	for _, dir := range []string{claude, shared} {
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".hermes")); !os.IsNotExist(err) {
		t.Fatal("an undetected harness got a folder")
	}
	st := Status(home)
	for _, h := range st.Harnesses {
		want := map[string]string{"claude-code": StateCurrent, "codex": StateCurrent, "pi": StateCurrent, "hermes": StateMissing}[h.Name]
		if h.State != want {
			t.Errorf("%s: state %s, want %s", h.Name, h.State, want)
		}
	}
	// settings files are never touched
	entries, _ := os.ReadDir(filepath.Join(home, ".claude"))
	if len(entries) != 1 || entries[0].Name() != "skills" {
		t.Fatalf("~/.claude got more than skills/: %v", entries)
	}
}

func TestInstallForcedAndDryRun(t *testing.T) {
	home := bare(t)
	rs, err := Install(Options{Home: home, Harnesses: []string{"claude", "hermes"}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].Action != "would install" {
		t.Fatalf("dry run: %+v", rs)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote")
	}
	if _, err := Install(Options{Home: home, Harnesses: []string{"hermes"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".hermes", "skills", "yonder", "SKILL.md")); err != nil {
		t.Fatal("forced install should write even without the harness")
	}
	if _, err := Install(Options{Home: home, Harnesses: []string{"nope"}}); err == nil {
		t.Fatal("unknown harness should error")
	}
}

func TestAtomicOverwrite(t *testing.T) {
	home := bare(t)
	dest := filepath.Join(home, ".claude", "skills", "yonder")
	if err := writeSkill(dest); err != nil {
		t.Fatal(err)
	}
	// stale extras and an outdated stamp from an older install
	if err := os.WriteFile(filepath.Join(dest, "old.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, versionFile), []byte("0.0.1 abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(dest)
	leftover := filepath.Join(parent, ".yonder-tmp-crashed")
	mkdir(t, leftover)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(leftover, past, past); err != nil {
		t.Fatal(err)
	}
	// a sibling skill must survive
	mkdir(t, filepath.Join(parent, "other-skill"))

	if s, v := stateOf(dest); s != StateOutdated || v != "0.0.1" {
		t.Fatalf("state %s %s", s, v)
	}
	rs, err := Install(Options{Home: home, Harnesses: []string{"claude-code"}})
	if err != nil || rs[0].Action != "updated" || rs[0].Err != nil {
		t.Fatalf("%+v %v", rs, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "old.md")); !os.IsNotExist(err) {
		t.Fatal("overwrite should replace the whole folder")
	}
	if s, _ := stateOf(dest); s != StateCurrent {
		t.Fatalf("state after update: %s", s)
	}
	entries, _ := os.ReadDir(parent)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "other-skill,yonder" {
		t.Fatalf("parent should hold only yonder and the sibling: %v", names)
	}
}

func TestSymlinkLeftAlone(t *testing.T) {
	home := bare(t)
	elsewhere := t.TempDir()
	parent := filepath.Join(home, ".agents", "skills")
	mkdir(t, parent)
	if err := os.Symlink(elsewhere, filepath.Join(parent, "yonder")); err != nil {
		t.Fatal(err)
	}
	rs, _ := Install(Options{Home: home, Harnesses: []string{"codex"}})
	if rs[0].Action != "left alone" {
		t.Fatalf("%+v", rs)
	}
	rs, _ = Uninstall(Options{Home: home})
	if r := byPath(rs)[filepath.Join(parent, "yonder")]; r.Action != "left alone" {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Lstat(filepath.Join(parent, "yonder")); err != nil {
		t.Fatal("symlink should survive")
	}
}

func TestUninstall(t *testing.T) {
	home := bare(t)
	if _, err := Install(Options{Home: home, Harnesses: []string{"claude-code", "codex"}}); err != nil {
		t.Fatal(err)
	}
	rs, err := Uninstall(Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(rs)
	if got[filepath.Join(home, ".claude", "skills", "yonder")].Action != "removed" ||
		got[filepath.Join(home, ".agents", "skills", "yonder")].Action != "removed" ||
		got[filepath.Join(home, ".hermes", "skills", "yonder")].Action != "not installed" {
		t.Fatalf("%+v", rs)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); err != nil {
		t.Fatal("uninstall should leave the skills dir itself")
	}
}

func TestHomeUnderYonderHome(t *testing.T) {
	t.Setenv("YONDER_HOME", "/tmp/ch")
	if Home() != "/tmp/ch/home" {
		t.Fatalf("Home() = %s", Home())
	}
}
