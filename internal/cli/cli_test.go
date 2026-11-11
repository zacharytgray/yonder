package cli

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/runlog"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

func TestExitCode(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("boom"), 1},
		{withCode(75, "gpu-box is offline"), 75},
		{quietExit(3), 3},
		{&usageError{errors.New("bad")}, 2},
		{fmt.Errorf("wrapped: %w", &usageError{errors.New("bad")}), 2},
		{errors.New(`unknown flag: --nope`), 2},
		{errors.New(`unknown command "x" for "yonder"`), 2},
		{&transport.UnreachableError{Target: "gpu-box"}, 75},
		{fmt.Errorf("refresh: %w", &transport.UnreachableError{Target: "gpu-box"}), 75},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	if quietExit(0) != nil {
		t.Fatal("exit 0 is no error")
	}
}

func TestOfflineMessage(t *testing.T) {
	m := registry.Machine{Name: "gpu-box", Notes: "sleeps at night"}
	err := offlineError(m, tailnet.Peer{LastSeen: time.Now().Add(-2*time.Hour - time.Minute)})
	want := "gpu-box is offline (last seen 2h ago) — notes: sleeps at night"
	if err.Error() != want || ExitCode(err) != 75 {
		t.Fatalf("got %q (%d)", err.Error(), ExitCode(err))
	}
	if got := offlineError(registry.Machine{Name: "x"}, tailnet.Peer{}).Error(); got != "x is offline (last seen unknown)" {
		t.Fatalf("got %q", got)
	}
}

func TestAgo(t *testing.T) {
	now := time.Now()
	cases := map[time.Duration]string{
		10 * time.Second: "just now",
		5 * time.Minute:  "5m ago",
		3 * time.Hour:    "3h ago",
		72 * time.Hour:   "3d ago",
	}
	for d, want := range cases {
		if got := ago(now.Add(-d)); got != want {
			t.Errorf("ago(-%v) = %q, want %q", d, got, want)
		}
	}
}

func TestCommandString(t *testing.T) {
	if commandString([]string{"cd x && make"}) != "cd x && make" {
		t.Fatal("one arg is a shell string")
	}
	if got := commandString([]string{"echo", "a b", "$HOME"}); got != `echo 'a b' '$HOME'` {
		t.Fatalf("got %s", got)
	}
}

func TestDetectHarness(t *testing.T) {
	for _, k := range []string{"CLAUDECODE", "CODEX_THREAD_ID", "CODEX_SANDBOX", "PI_CODING_AGENT", "HERMES_SESSION_ID", "AI_AGENT"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if detectHarness() != "shell" {
		t.Fatal("default is shell")
	}
	t.Setenv("PI_CODING_AGENT", "true")
	if detectHarness() != "pi" {
		t.Fatal("pi")
	}
	t.Setenv("CODEX_THREAD_ID", "thread")
	if detectHarness() != "codex" {
		t.Fatal("codex")
	}
	t.Setenv("CLAUDECODE", "1")
	if detectHarness() != "claude-code" {
		t.Fatal("claude-code")
	}
}

func TestView(t *testing.T) {
	st := &tailnet.Status{
		Self: tailnet.Peer{DNSName: "laptop.example.ts.net", Online: true},
		Peers: []tailnet.Peer{
			{DNSName: "gpu-box.example.ts.net", Online: false, LastSeen: time.Now().Add(-time.Hour)},
			{DNSName: "desk.example.ts.net", Online: true},
		},
	}
	old := time.Now().Add(-10 * 24 * time.Hour)
	gpu := registry.Machine{Name: "gpu-box", DiscoveredAt: old, Facts: registry.Facts{
		CPUCores: 16, MemoryGB: 64,
		GPUs:      []registry.GPU{{Name: "Example GPU"}, {Name: "Example GPU"}},
		Models:    []registry.Model{{Name: "a"}, {Name: "b"}},
		Harnesses: []registry.Harness{{Name: "codex"}},
	}}
	v := view(gpu, "laptop", st, nil)
	if v.State != "offline" || v.LastSeen == nil || !v.Stale || v.ModelCount != 2 || v.Harnesses[0] != "codex" {
		t.Fatalf("gpu view %+v", v)
	}
	if v.Hardware != "16 cores, 64 GB RAM, 2× Example GPU" {
		t.Fatalf("hardware %q", v.Hardware)
	}
	if v := view(registry.Machine{Name: "laptop", DiscoveredAt: time.Now()}, "laptop", st, nil); v.State != "self" || v.Stale {
		t.Fatalf("self view %+v", v)
	}
	if v := view(registry.Machine{Name: "desk", Pending: true}, "laptop", st, nil); v.State != "pending" || v.Stale {
		t.Fatalf("pending view %+v", v)
	}
	if v := view(registry.Machine{Name: "gone"}, "laptop", st, nil); v.State != "missing" {
		t.Fatalf("missing view %+v", v)
	}
	if v := view(registry.Machine{Name: "desk"}, "laptop", nil, errors.New("no tailscale")); v.State != "unknown" || v.Online != nil {
		t.Fatalf("unknown view %+v", v)
	}
}

func TestFilterLog(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	es := []runlog.Entry{
		{Time: t0.Add(2 * time.Minute), ID: "c", From: "laptop", To: "desk", Side: "target"},
		{Time: t0, ID: "a", From: "laptop", To: "gpu-box", Side: "caller"},
		{Time: t0.Add(time.Minute), ID: "b", From: "desk", To: "laptop", Side: "target"},
		{Time: t0.Add(time.Minute), ID: "b", From: "desk", To: "laptop", Side: "target"}, // dup
	}
	got := filterLog(es, "", 0)
	if len(got) != 3 || got[0].ID != "a" || got[2].ID != "c" {
		t.Fatalf("got %+v", got)
	}
	if got := filterLog(es, "gpu-box", 0); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("machine filter %+v", got)
	}
	if got := filterLog(es, "", 2); len(got) != 2 || got[0].ID != "b" {
		t.Fatalf("limit keeps newest %+v", got)
	}
}

func TestParseProbe(t *testing.T) {
	p := parseProbe("yonder=/home/me/.local/bin/yonder\nos=Linux\narch=x86_64\nhome=/home/me\npub=ssh-ed25519 AAAA me@gpu-box\njunk line\n")
	if p.yonder != "/home/me/.local/bin/yonder" || p.os != "linux" || p.arch != "amd64" || p.home != "/home/me" || len(p.pubs) != 1 {
		t.Fatalf("%+v", p)
	}
	if parseProbe("os=Darwin\narch=arm64\n").os != "darwin" {
		t.Fatal("darwin")
	}
}
