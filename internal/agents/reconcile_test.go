package agents

import (
	"strings"
	"testing"
)

func TestTrimSectionsKeepsBlockAndRest(t *testing.T) {
	block := blockBegin + "\n## Yonder briefing\nrendered\n" + blockEnd + "\n"
	in := "# Style\nbe brief\n\n# Laptop\nbattery\n\n" + block + "\n# Models\nbig one\n\n# Tail\nlast para\n"
	out, removed, err := TrimSections(in, []string{"# style", "# models"})
	if err != nil {
		t.Fatal(err)
	}
	want := "# Laptop\nbattery\n\n" + block + "\n# Tail\nlast para\n"
	if out != want {
		t.Fatalf("got\n%q\nwant\n%q", out, want)
	}
	if strings.Join(removed, ",") != "# style,# models" {
		t.Fatalf("removed %v", removed)
	}
	// nothing to take out: unchanged, byte for byte
	if out2, r, _ := TrimSections(in, []string{"# nope"}); out2 != in || r != nil {
		t.Fatal("unchanged file changed")
	}
	// an open block is refused
	if _, _, err := TrimSections("# A\nx\n"+blockBegin+"\n", []string{"# a"}); err == nil {
		t.Fatal("open block should be refused")
	}
	// a file yonder rendered has nothing of the user's
	r := renderMark + "\n# Style\nbe brief\n"
	if out, _, _ := TrimSections(r, []string{"# style"}); out != r {
		t.Fatal("rendered file changed")
	}
}

func TestUpsertSection(t *testing.T) {
	cur := "# Style\nold\n\n# Other\nkeep\n"
	got := upsertSection(cur, "# style", "# Style\nnew")
	if got != "# Style\nnew\n\n# Other\nkeep\n" {
		t.Fatalf("replace: %q", got)
	}
	if got := upsertSection(cur, "# added", "# Added\nx"); got != cur+"\n# Added\nx\n" {
		t.Fatalf("append: %q", got)
	}
	if got := upsertSection("", "# a", "# A\nx"); got != "# A\nx\n" {
		t.Fatalf("empty: %q", got)
	}
}

func reconcileFixture() GatherReport {
	block := "\n" + blockBegin + "\nrendered\n" + blockEnd + "\n"
	return Compare([]MachineFiles{
		{Machine: "laptop", Files: map[string][]byte{
			".claude/CLAUDE.md": []byte("# Style\nbe brief\n\n# Models\nuse the big one\n\n# Laptop\nbattery\n\n# Scratch\ntodo\n" + block),
		}},
		{Machine: "gpu-box", Files: map[string][]byte{
			".claude/CLAUDE.md": []byte("# Style\nbe brief\n\n# Models\nuse the small one\n\n# Gpu\ncuda 12\n"),
			".codex/AGENTS.md":  []byte("# Style\nbe very brief\n"),
		}},
	})
}

func TestBuildReconcile(t *testing.T) {
	r := reconcileFixture()
	p := Plan{V: 1, Fingerprint: r.Fingerprint, Sections: []PlanSection{
		{Key: "# style", Action: ActShared, From: "laptop"},
		{Key: "# models", Action: ActMerge, Text: "# Models\nuse the big one for planning, the small one for scouting"},
		{Key: "# laptop", Action: ActPerMachine},
		{Key: "# gpu", Action: ActPerMachine},
		{Key: "# scratch", Action: ActDrop},
	}}
	store := map[string]string{"instructions.md": "# Old\nkept\n"}
	rc, err := BuildReconcile(r, p, func(rel string) (string, bool) { s, ok := store[rel]; return s, ok })
	if err != nil {
		t.Fatal(err)
	}
	if got := rc.Store["instructions.md"]; got != "# Old\nkept\n\n# Style\nbe brief\n\n# Models\nuse the big one for planning, the small one for scouting\n" {
		t.Fatalf("instructions.md: %q", got)
	}
	if rc.Store["machines/laptop/instructions.md"] != "# Laptop\nbattery\n" || rc.Store["machines/gpu-box/instructions.md"] != "# Gpu\ncuda 12\n" {
		t.Fatalf("overlays: %q", rc.Store)
	}
	lt := rc.Trims["laptop"]
	if len(lt) != 1 || lt[0].Path != ".claude/CLAUDE.md" || len(lt[0].Remove) != 4 || lt[0].Hash == "" {
		t.Fatalf("laptop trims: %+v", lt)
	}
	gt := rc.Trims["gpu-box"]
	if len(gt) != 2 || gt[1].Path != ".codex/AGENTS.md" || strings.Join(gt[1].Remove, ",") != "# style" {
		t.Fatalf("gpu-box trims: %+v", gt)
	}
}

func TestBuildReconcileRefuses(t *testing.T) {
	r := reconcileFixture()
	none := func(string) (string, bool) { return "", false }
	for name, p := range map[string]Plan{
		"version":       {V: 2},
		"fingerprint":   {V: 1, Fingerprint: "stale"},
		"unknown key":   {V: 1, Sections: []PlanSection{{Key: "# nope", Action: ActDrop}}},
		"twice":         {V: 1, Sections: []PlanSection{{Key: "# gpu", Action: ActDrop}, {Key: "# gpu", Action: ActDrop}}},
		"bad action":    {V: 1, Sections: []PlanSection{{Key: "# gpu", Action: "keep"}}},
		"no from":       {V: 1, Sections: []PlanSection{{Key: "# style", Action: ActShared}}},
		"two versions":  {V: 1, Sections: []PlanSection{{Key: "# style", Action: ActShared, From: "gpu-box"}}},
		"empty merge":   {V: 1, Sections: []PlanSection{{Key: "# models", Action: ActMerge}}},
		"marker merge":  {V: 1, Sections: []PlanSection{{Key: "# models", Action: ActMerge, Text: "<!-- yonder:end -->"}}},
		"ambiguous own": {V: 1, Sections: []PlanSection{{Key: "# style", Action: ActPerMachine}}},
	} {
		if _, err := BuildReconcile(r, p, none); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// one file of a machine with two versions is fine
	p := Plan{V: 1, Sections: []PlanSection{{Key: "# style", Action: ActShared, From: "gpu-box:~/.codex/AGENTS.md"}}}
	rc, err := BuildReconcile(r, p, none)
	if err != nil || !strings.Contains(rc.Store["instructions.md"], "be very brief") {
		t.Fatalf("%v %q", err, rc.Store)
	}
}
