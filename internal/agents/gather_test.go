package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatherScriptRoundTrip(t *testing.T) {
	home := t.TempDir()
	for p, c := range map[string]string{
		".claude/CLAUDE.md":   "# Style\nbe brief\n\n=== not a header\n",
		".pi/agent/AGENTS.md": "no newline at end",
	} {
		must(t, os.MkdirAll(filepath.Join(home, filepath.Dir(p)), 0o755))
		must(t, os.WriteFile(filepath.Join(home, p), []byte(c), 0o644))
	}
	cmd := exec.Command("sh", "-c", GatherScript())
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	must(t, err)
	got, err := ParseGather(strings.NewReader(string(out)))
	must(t, err)
	if len(got) != 2 || string(got[".claude/CLAUDE.md"]) != "# Style\nbe brief\n\n=== not a header\n" || string(got[".pi/agent/AGENTS.md"]) != "no newline at end" {
		t.Fatalf("parsed: %q", got)
	}
	here := GatherHere(home)
	if len(here) != 2 || string(here[".claude/CLAUDE.md"]) != string(got[".claude/CLAUDE.md"]) {
		t.Fatalf("here: %q", here)
	}
	if _, err := ParseGather(strings.NewReader("garbage\n")); err == nil {
		t.Fatal("garbage should not parse")
	}
}

func TestSplitSections(t *testing.T) {
	ss := splitSections("intro one\nstill intro\n\nsecond para\n\n# Style\nbe brief  \n\n\n\nno emoji\n## Notes\nx\n## Notes\ny\n")
	var keys []string
	for _, s := range ss {
		keys = append(keys, s.key)
	}
	want := []string{"¶ intro one", "¶ second para", "# style", "## notes", "## notes #2"}
	if strings.Join(keys, "|") != strings.Join(want, "|") {
		t.Fatalf("keys: %q", keys)
	}
	if ss[2].text != "be brief\n\nno emoji" {
		t.Fatalf("normalized body: %q", ss[2].text)
	}
}

func TestCompare(t *testing.T) {
	block := "\n" + blockBegin + "\n## Yonder briefing\nrendered\n" + blockEnd + "\n"
	r := Compare([]MachineFiles{
		{Machine: "laptop", Files: map[string][]byte{
			".claude/CLAUDE.md": []byte("# Style\nbe brief\n\n# Models\nuse the big one\n\n# Laptop only\nbattery\n" + block),
			".codex/AGENTS.md":  []byte(renderMark + "\n\nall yonder's\n"),
		}},
		{Machine: "desk", Files: map[string][]byte{
			".claude/CLAUDE.md": []byte("# Style\nbe brief\n\n# Models\nuse the small one\n"),
			".codex/AGENTS.md":  []byte("# Style\nbe brief\n"),
		}},
		{Machine: "gpu-box", Err: "offline"},
	})
	got := map[string]string{}
	for _, s := range r.Sections {
		got[s.Key] = s.Status
	}
	if got["# style"] != SameEverywhere || got["# models"] != Differs || got["# laptop only"] != OnOne || len(got) != 3 {
		t.Fatalf("statuses: %v", got)
	}
	for _, s := range r.Sections {
		if s.Key == "# style" && (len(s.Versions) != 1 || len(s.Versions[0].Files) != 3) {
			t.Fatalf("one version, in three files: %+v", s)
		}
		if strings.Contains(s.Versions[0].Text, "rendered") {
			t.Fatal("yonder's block is never gathered")
		}
	}
	if !r.Diverged() {
		t.Fatal("two machines that differ")
	}
	if r.Machines[1].Name != "gpu-box" || r.Machines[1].Error != "offline" {
		t.Fatalf("machines: %+v", r.Machines)
	}
	for _, f := range r.Machines[2].Files {
		if f.Harness == "codex" && !f.Rendered {
			t.Fatalf("a rendered file is yonder's: %+v", f)
		}
	}
	// the same everywhere is nothing to reconcile
	same := Compare([]MachineFiles{
		{Machine: "a", Files: map[string][]byte{".claude/CLAUDE.md": []byte("x\n")}},
		{Machine: "b", Files: map[string][]byte{".claude/CLAUDE.md": []byte("x\n")}},
	})
	if same.Diverged() || same.Sections[0].Status != SameEverywhere {
		t.Fatalf("same: %+v", same)
	}
	if one := Compare([]MachineFiles{{Machine: "a", Files: map[string][]byte{".claude/CLAUDE.md": []byte("x\n")}}, {Machine: "b"}}); one.Diverged() {
		t.Fatal("one machine with files has nothing to reconcile")
	}
}
