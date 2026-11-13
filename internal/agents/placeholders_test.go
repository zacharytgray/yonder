package agents

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaceholdersPerHarness(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, RenderFile, "[claude]\ntop = \"Atlas\"\nfast = \"Pebble\"\n\n[codex]\ntop = \"Zephyr\"\nfast = \"Quill\"\n")
	f.put(f.storeDir, "instructions.md", "Plan with {{model:top}}; scout with {{model:fast}}.\nThe top model stays top. {{ model:top }} and {{model:Top}} are left alone.\n")
	f.put(f.storeDir, BriefingFile, "reviewed: 2026-11-01\n- local jobs use {{model:fast}}\n")
	l := f.linker()
	must(t, l.Link([]string{"claude-code", "codex"}))
	claude := read(filepath.Join(f.home, ".claude/CLAUDE.md"))
	codex := read(filepath.Join(f.home, ".codex/AGENTS.md"))
	if !strings.Contains(claude, "Plan with Atlas; scout with Pebble.") || !strings.Contains(claude, "local jobs use Pebble") {
		t.Fatalf("claude:\n%s", claude)
	}
	if !strings.Contains(codex, "Plan with Zephyr; scout with Quill.") || !strings.Contains(codex, "local jobs use Quill") {
		t.Fatalf("codex:\n%s", codex)
	}
	for _, text := range []string{claude, codex} {
		if !strings.Contains(text, "The top model stays top. {{ model:top }} and {{model:Top}} are left alone.") {
			t.Fatalf("only the exact syntax is replaced:\n%s", text)
		}
	}
	if u := l.Unresolved(); len(u) != 0 {
		t.Fatalf("unresolved: %v", u)
	}

	// a tier codex has no name for: codex's file keeps its text, claude's renders
	f.put(f.storeDir, RenderFile, "[claude]\ntop = \"Atlas\"\nfast = \"Pebble\"\n\n[codex]\ntop = \"Zephyr\"\n")
	f.put(f.storeDir, "instructions.md", "Plan with {{model:top}}; scout with {{model:fast}}; review with {{model:deep}}.\n")
	err := l.Link(nil)
	if err == nil || !strings.Contains(err.Error(), "agents/instructions.md uses {{model:deep}}, which agents/render.toml doesn't define, and {{model:fast}}, which it names for other harnesses only; nothing was rendered for codex; add fast = \"...\", deep = \"...\" under [codex]") {
		t.Fatalf("want codex refused: %v", err)
	}
	if read(filepath.Join(f.home, ".codex/AGENTS.md")) != codex {
		t.Fatal("codex's file should keep what it had")
	}
	if u := l.Unresolved(); len(u) != 2 || u[0] != "claude: deep" || u[1] != "codex: deep, fast" {
		t.Fatalf("unresolved: %v", u)
	}
}

func TestRenderTableChecks(t *testing.T) {
	for _, bad := range []string{
		"top = \"Atlas\"\n",                   // not under a harness
		"[gemini]\ntop = \"Atlas\"\n",         // not a harness yonder renders for
		"[claude]\ntop = 3\n",                 // not a string
		"[claude]\nTop = \"Atlas\"\n",         // not a tier name
		"[claude]\ntop = \"{{model:top}}\"\n", // no nesting
		"[claude\n",
	} {
		if r := Check(RenderFile, []byte(bad)); r == nil || !r.Final {
			t.Errorf("%q should be refused: %+v", bad, r)
		}
	}
	for _, bad := range []string{"[shared]\nhermes = \"yes\"\n", "[shared]\nclaude = true\n", "shared = true\n"} {
		if r := Check(RenderFile, []byte(bad)); r == nil || !r.Final {
			t.Errorf("%q should be refused: %+v", bad, r)
		}
	}
	// regression (review of a68f72a): escape and line-separator characters
	for _, bad := range []string{"[claude]\ntop = \"a\\u001b[2J\"\n", "[claude]\ntop = \"a\\u2028b\"\n", "[claude]\ntop = \"a\\u2029b\"\n", "[claude]\ntop = \"a\\u202eb\"\n"} {
		if r := Check(RenderFile, []byte(bad)); r == nil || !r.Final {
			t.Errorf("%q should be refused: %+v", bad, r)
		}
	}
	if r := Check(RenderFile, []byte("[shared]\nhermes = false\n[claude]\ntop = \"Atlas\"\n[pi]\nfast = \"Pebble 2\"\n")); r != nil {
		t.Errorf("a good table: %+v", r)
	}
	if got := Placeholders("{{model:a}} {{model:b}} {{model:a}} {{model:}}"); strings.Join(got, ",") != "a,b" {
		t.Errorf("placeholders: %v", got)
	}
}

// regression (recheck of fc24fe5): one harness's briefing fails on a missing
// tier while another's renders; the usage keeps the failure, and the message
// says only the briefing was left out
func TestBriefingTierFailureShowsInUsage(t *testing.T) {
	f := newFixture(t)
	f.put(f.storeDir, RenderFile, "[claude]\nfast = \"Pebble\"\n")
	f.put(f.storeDir, BriefingFile, "reviewed: 2026-11-01\n- local jobs use {{model:fast}}\n")
	f.put(f.storeDir, "instructions.md", "shared rules\n")
	l := f.linker()
	l.Registry = f.registry(exampleMachines()...)
	for _, order := range [][]string{{"codex", "claude-code"}, {"claude-code", "codex"}} {
		err := l.Link(order)
		if err == nil || !strings.Contains(err.Error(), "the briefing was left out for codex (the rest of its instructions render)") || strings.Contains(err.Error(), "nothing was rendered") {
			t.Fatalf("%v: %v", order, err)
		}
		if u := l.BriefingUsage(); !strings.Contains(u.Error, "left out for codex") {
			t.Fatalf("%v: usage lost the failure: %+v", order, u)
		}
	}
	if !strings.Contains(read(filepath.Join(f.home, ".codex/AGENTS.md")), "shared rules") || !strings.Contains(read(filepath.Join(f.home, ".claude/CLAUDE.md")), "local jobs use Pebble") {
		t.Fatal("both harnesses render the rest")
	}
}
