package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
)

func exampleMachines() []registry.Machine {
	return []registry.Machine{
		{Name: "laptop", OS: "darwin", Arch: "arm64", TailscaleIP: "100.64.0.1", SSHUser: "me", Role: "laptop",
			Facts: registry.Facts{OSVersion: "macOS 15.1", Arch: "arm64", CPU: "Apple M3 Pro", MemoryGB: 36, Laptop: true,
				GPUs: []registry.GPU{{Name: "Apple M3 Pro", MemoryGB: 36, Unified: true}}}},
		{Name: "gpu-box", OS: "linux", Arch: "amd64", TailscaleIP: "100.64.0.2", SSHUser: "me", Role: "gpu", Notes: "sleeps at night",
			Facts: registry.Facts{OSVersion: "Ubuntu 24.04.1 LTS", Arch: "amd64", CPU: "AMD Ryzen 9 7950X 16-Core Processor", MemoryGB: 64,
				GPUs:   []registry.GPU{{Name: "NVIDIA GeForce RTX 4090", MemoryGB: 24}},
				Models: []registry.Model{{Name: "llama3:8b", Runtime: "ollama", Serving: true, Port: 11434}, {Name: "idle-model"}}}},
		{Name: "nas", OS: "linux", Arch: "arm64", TailscaleIP: "100.64.0.3", SSHUser: "me", Role: "server"},
	}
}

func (f *fixture) registry(ms ...registry.Machine) *registry.Store {
	f.t.Helper()
	s := &registry.Store{Dir: filepath.Join(filepath.Dir(f.home), "config")}
	for _, m := range ms {
		must(f.t, s.SaveMachine(m))
	}
	return s
}

func TestGeneratedBriefing(t *testing.T) {
	got := GeneratedBriefing(exampleMachines(), "laptop")
	for _, want := range []string{
		"## Yonder briefing\n",
		"- `gpu-box` gpu · 100.64.0.2 · Ubuntu 24.04.1 LTS amd64 · AMD Ryzen 9 7950X, RTX 4090 24 GB, 64 GB RAM · serves llama3:8b (ollama :11434) · sleeps\n",
		"- `laptop` laptop, this machine · 100.64.0.1 · macOS 15.1 arm64 · M3 Pro, 36 GB RAM · travels\n",
		"- `nas` server · 100.64.0.3 · linux arm64 · always-on\n",
		"All reached as user `me` over Tailscale.",
		"Exit 75 means",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "idle-model") {
		t.Error("only served models are listed")
	}
	// gpu-box sorts first: the list is by name
	if strings.Index(got, "gpu-box") > strings.Index(got, "`laptop`") {
		t.Error("machines are listed by name")
	}
	ms := exampleMachines()
	ms[2].SSHUser = "admin"
	if got := GeneratedBriefing(ms, ""); !strings.Contains(got, "admin@100.64.0.3") || strings.Contains(got, "All reached as user") {
		t.Errorf("different users go on each line:\n%s", got)
	}
	if GeneratedBriefing(nil, "laptop") != "" {
		t.Error("no records, no list")
	}
}

func TestParseBriefingReviewedDates(t *testing.T) {
	secs, text := parseBriefing([]byte("reviewed: 2026-10-01\nintro\n\n## Routing\n- a\n\n## Places\nreviewed: 2026-01-02\n- b\n"))
	if strings.Contains(text, "reviewed:") || !strings.HasPrefix(text, "intro") || !strings.Contains(text, "## Places\n- b") {
		t.Fatalf("text: %q", text)
	}
	if len(secs) != 3 {
		t.Fatalf("sections: %+v", secs)
	}
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	if !secs[0].Reviewed.Equal(day("2026-10-01")) || !secs[1].Reviewed.Equal(day("2026-10-01")) || !secs[2].Reviewed.Equal(day("2026-01-02")) {
		t.Fatalf("dates: %+v", secs)
	}
	if secs[1].Heading != "Routing" || secs[1].Line != 4 {
		t.Fatalf("heading: %+v", secs[1])
	}
}

func TestBriefingRendersFirstInTheBlock(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, "instructions.md", "shared rules\n")
	f.put(f.storeDir, BriefingFile, "reviewed: 2026-11-01\n## Routing\n- heavy jobs go to `gpu-box`\n")
	f.put(f.home, ".claude/CLAUDE.md", "my own rules\n")
	l := f.linker()
	l.Registry = f.registry(exampleMachines()...)
	must(t, l.Link([]string{"claude-code", "codex"}))
	got := read(filepath.Join(f.home, ".claude/CLAUDE.md"))
	b, r, s := strings.Index(got, "## Yonder briefing"), strings.Index(got, "## Routing"), strings.Index(got, "shared rules")
	if !strings.HasPrefix(got, "my own rules\n") || b < 0 || b >= r || r >= s || strings.Contains(got, "reviewed:") {
		t.Fatalf("block:\n%s", got)
	}
	if codex := read(filepath.Join(f.home, ".codex/AGENTS.md")); !strings.Contains(codex, "`gpu-box` gpu") {
		t.Fatalf("codex gets it too:\n%s", codex)
	}

	// a registry change renders again on the next relink
	ms := exampleMachines()
	ms[1].Role = "server"
	l.Registry = f.registry(ms...)
	must(t, l.Link(nil))
	if got := read(filepath.Join(f.home, ".claude/CLAUDE.md")); !strings.Contains(got, "`gpu-box` server") {
		t.Fatalf("not rendered again:\n%s", got)
	}
}

func TestBriefingOverCapRendersNothing(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, BriefingFile, "## Routing\n- fine\n")
	f.put(f.home, ".claude/CLAUDE.md", "mine\n")
	l := f.linker()
	l.Registry = f.registry(exampleMachines()...)
	must(t, l.Link([]string{"claude-code"}))
	before := read(filepath.Join(f.home, ".claude/CLAUDE.md"))

	// the hand-written part over 2 KB: refused, and the file keeps what it had
	f.put(f.storeDir, BriefingFile, "## Big\n"+strings.Repeat("- a line of routing detail nobody needs\n", 60))
	err := l.Link(nil)
	if err == nil || !strings.Contains(err.Error(), "over its 2.0 KB cap") || !strings.Contains(err.Error(), `"Big"`) {
		t.Fatalf("want the hand cap: %v", err)
	}
	if got := read(filepath.Join(f.home, ".claude/CLAUDE.md")); got != before {
		t.Fatalf("nothing should change:\n%s", got)
	}
	if bl := l.Blocked(); len(bl) != 1 {
		t.Fatalf("blocked: %v", bl)
	}
	if u := l.BriefingUsage(); u.Error == "" || u.HandBytes <= HandCap {
		t.Fatalf("usage: %+v", u)
	}
	if r := Check(BriefingFile, []byte(read(filepath.Join(f.storeDir, BriefingFile)))); r == nil || !r.Final {
		t.Fatalf("an over-cap briefing.md doesn't sync, allowed or not: %+v", r)
	}

	// under the hand cap but over 4 KB in all, from many machines
	f.put(f.storeDir, BriefingFile, "## Routing\n- fine\n")
	var many []registry.Machine
	for i := 0; i < 40; i++ {
		m := exampleMachines()[1]
		m.Name = "gpu-box-" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		many = append(many, m)
	}
	l.Registry = f.registry(many...)
	if err := l.Link(nil); err == nil || !strings.Contains(err.Error(), "over its 4.0 KB cap") {
		t.Fatalf("want the total cap: %v", err)
	}
	if got := read(filepath.Join(f.home, ".claude/CLAUDE.md")); got != before {
		t.Fatal("nothing should change")
	}
}

func TestHermesGetsABlockInItsOwnSoulFile(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, "instructions.md", "shared rules\n")
	l := f.linker()
	l.Registry = f.registry(exampleMachines()...)
	must(t, l.Link([]string{"hermes"}))
	soul := filepath.Join(f.home, ".hermes/SOUL.md")
	if _, err := os.Stat(soul); err == nil {
		t.Fatal("yonder must not create SOUL.md: hermes seeds its own")
	}
	f.put(f.home, ".hermes/SOUL.md", "You are Hermes.\n")
	must(t, l.Link([]string{"hermes"}))
	got := read(soul)
	if !strings.HasPrefix(got, "You are Hermes.\n") || !strings.Contains(got, "## Yonder briefing") || !strings.Contains(got, "shared rules") {
		t.Fatalf("SOUL.md:\n%s", got)
	}
	must(t, l.Unlink(false))
	if got := read(soul); got != "You are Hermes.\n" {
		t.Fatalf("unlink leaves the user's file: %q", got)
	}
}

func TestHomePathsOnlyThisMachinesHome(t *testing.T) {
	data := []byte("a /home/me/x\nb ~/x\nc /Users/other/x\nd `gpu-box:/home/me/models`\ne /home/meadow/x\nf see /home/me\n")
	got := HomePaths(data, "/home/me")
	if len(got) != 2 || got[0] != 1 || got[1] != 6 {
		t.Fatalf("lines: %v", got)
	}
	if HomePaths(data, "") != nil {
		t.Fatal("no home, no warning")
	}
}

func TestCheckBriefing(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	data := []byte(strings.Join([]string{
		"reviewed: 2026-11-01",
		"## Routing",
		"- heavy jobs on `gpu-box` at 100.64.0.2", // 3: repeats its own address
		"- `laptop` is the server",                // 4: contradicts the role
		"- run `yonder run old-box -- ls`",        // 5: removed machine
		"- `ghost:~/x` and 100.64.0.9",            // 6: unknown machine, unknown address
		"- `nas` has 100.64.0.2",                  // 7: someone else's address
		"## Old",
		"reviewed: 2026-01-01",
		"- a",
		"## Never",
		"- b",
	}, "\n"))
	ms := exampleMachines()
	ms[0].Role = "laptop"
	fs := CheckBriefing(data, ms, []string{"old-box"}, now)
	want := map[string]bool{
		"line 3 repeats gpu-box's address":                           false,
		"line 4 calls laptop the server, but its record says laptop": false,
		"line 5 names old-box, which was removed":                    false,
		"line 6 names ghost, which is not a fleet machine":           false,
		"line 6 names the address 100.64.0.9, which no machine has":  false,
		"line 7 puts 100.64.0.2, which is gpu-box's address":         false,
		`section "Old" was last reviewed 316 days ago`:               false,
		`section "Never" was last reviewed`:                          false, // falls back to the file's date: fine
	}
	for _, f := range fs {
		for w := range want {
			if strings.Contains(f.Detail, w) {
				want[w] = true
			}
		}
	}
	for w, ok := range want {
		if w == `section "Never" was last reviewed` {
			if ok {
				t.Error("a section without its own date uses the file's")
			}
			continue
		}
		if !ok {
			t.Errorf("missing finding %q in %+v", w, fs)
		}
	}
	// a store name after sync, and a machine about to be added, are not machine names
	if fs := CheckBriefing([]byte("reviewed: 2026-11-01\n- `yonder sync agents`, `yonder add new-box`\n"), ms, nil, now); len(fs) != 0 {
		t.Fatalf("false positives: %+v", fs)
	}
	// no date anywhere
	fs = CheckBriefing([]byte("## Routing\n- a\n"), ms, nil, now)
	if len(fs) != 1 || !strings.Contains(fs[0].Detail, "has no reviewed: date") {
		t.Fatalf("undated: %+v", fs)
	}
}

func TestBriefingPathsAndDraft(t *testing.T) {
	data := []byte("- repos `~/repos/<repo>`, models `gpu-box:/srv/models`, `ghost:~/x`, `yonder ls ~/x`\n- hub at 100.64.0.1 (100.64.0.1)\n")
	refs := BriefingPaths(data, exampleMachines())
	if len(refs) != 2 || refs[0].Path != "~/repos" || refs[0].Machine != "" || refs[1].Machine != "gpu-box" || refs[1].Path != "/srv/models" {
		t.Fatalf("refs: %+v", refs)
	}
	got := Draft(string(data), []Finding{{Line: 2, Cut: "100.64.0.1"}})
	if !strings.HasSuffix(got, "- hub\n") {
		t.Fatalf("draft: %q", got)
	}
	if Draft("a\n- `x`\n", []Finding{{Line: 2, Cut: "- `x`"}}) != "a\n" {
		t.Fatal("a line cut whole goes")
	}
}

func TestDuplicatesWithTheUsersOwnText(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, BriefingFile, "reviewed: 2026-11-01\n- heavy jobs go to the gpu box, never the laptop\n")
	f.put(f.home, ".claude/CLAUDE.md", "my rules\n* Heavy jobs go to the gpu box, never the laptop.\n")
	l := f.linker()
	l.Registry = f.registry(exampleMachines()...)
	must(t, l.Link([]string{"claude-code"}))
	ds := l.Duplicates()
	if len(ds) != 1 || !strings.Contains(ds[0].Detail, "line 2 of agents/briefing.md") || !strings.Contains(ds[0].Fix, "~/.claude/CLAUDE.md") {
		t.Fatalf("duplicates: %+v", ds)
	}
}
