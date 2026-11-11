package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "log.jsonl")
	t0 := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	entries := []Entry{
		{Time: t0.Add(2 * time.Minute), ID: "b", From: "laptop", To: "gpu-box", Harness: "codex", Command: "nvidia-smi", Exit: Exit(0)},
		{Time: t0, ID: "a", From: "laptop", To: "gpu-box", Harness: "shell", Command: "true", Secrets: []string{"API_KEY"}, Exit: Exit(75), Side: SideCaller, Error: "offline"},
		{Time: t0.Add(time.Minute), ID: "c", From: "laptop", To: "gpu-box", Command: "sleep 9", Detached: true},
	}
	for _, e := range entries {
		if err := Append(path, e); err != nil {
			t.Fatal(err)
		}
	}
	// a torn line from a crash must not break reading
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("{not json\n")
	f.Close()

	got, err := Read(path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "a" || got[1].ID != "c" || got[2].ID != "b" {
		t.Fatalf("want sorted a,c,b: %+v", got)
	}
	if got[0].Side != SideCaller || *got[0].Exit != 75 || got[0].Secrets[0] != "API_KEY" {
		t.Fatalf("fields lost: %+v", got[0])
	}
	if got[2].Side != SideTarget {
		t.Fatal("side defaults to target")
	}
	if got[1].Exit != nil {
		t.Fatal("detached launch has null exit")
	}

	since, _ := Read(path, t0.Add(time.Minute))
	if len(since) != 2 {
		t.Fatalf("since filter: %d", len(since))
	}

	raw, _ := os.ReadFile(path)
	line := strings.Split(string(raw), "\n")[2]
	if !strings.Contains(line, `"exit":null`) || !strings.Contains(line, `"secrets":[]`) {
		t.Fatalf("json shape: %s", line)
	}
}

func TestReadMissing(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "none.jsonl"), time.Time{})
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
