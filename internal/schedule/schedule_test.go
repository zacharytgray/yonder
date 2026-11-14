package schedule

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func testJob(t *testing.T, goos string) Job {
	t.Helper()
	root := t.TempDir()
	t.Setenv("YONDER_HOME", filepath.Join(root, "yh"))
	bin := filepath.Join(root, "bin dir", "yonder")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// the same dirs Here uses under YONDER_HOME
	return Job{OS: goos, Bin: bin, Self: "gpu-box", Home: filepath.Join(root, "yh", "home"), StateDir: filepath.Join(root, "yh", "state"),
		Env: map[string]string{"XDG_STATE_HOME": "/x/state"}}
}

func TestPlist(t *testing.T) {
	j := testJob(t, "darwin")
	p := j.Plist()
	for _, want := range []string{
		"<string>" + Label + "</string>", Marker, "<string>sync</string>", "<string>--scheduled</string>",
		"<key>SHLVL</key>", "<key>RunAtLoad</key>", "<key>XDG_STATE_HOME</key>", LogPath(j.StateDir),
		"<string>" + filepath.Dir(j.Bin) + ":",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "StartInterval") {
		t.Error("StartInterval skips runs missed in sleep; want StartCalendarInterval")
	}
	if n := strings.Count(p, "<key>Minute</key>"); n != 4 {
		t.Errorf("want 4 calendar minutes, got %d", n)
	}
}

func TestSystemdUnits(t *testing.T) {
	j := testJob(t, "linux")
	svc, tm := j.Service(), j.Timer()
	for _, want := range []string{Marker, `ExecStart="` + j.Bin + `" sync --scheduled`, "Type=oneshot", "StandardOutput=append:" + LogPath(j.StateDir)} {
		if !strings.Contains(svc, want) {
			t.Errorf("service lacks %q:\n%s", want, svc)
		}
	}
	for _, want := range []string{"OnCalendar=*:", "/15", "Persistent=true", "OnStartupSec=", "WantedBy=timers.target"} {
		if !strings.Contains(tm, want) {
			t.Errorf("timer lacks %q:\n%s", want, tm)
		}
	}
	if strings.Contains(svc, "SHLVL") {
		t.Error("SHLVL is a macOS tailscale quirk")
	}
}

func TestOffsetSpreads(t *testing.T) {
	seen := map[int]bool{}
	for _, n := range []string{"laptop", "gpu-box", "server", "desktop", "pi", "mini", "studio"} {
		o := Offset(n)
		if o < 0 || o >= 15 {
			t.Fatalf("offset %d out of range", o)
		}
		seen[o] = true
		if Offset(n) != o {
			t.Fatal("offset must be stable")
		}
	}
	if len(seen) < 3 {
		t.Errorf("offsets barely spread: %v", seen)
	}
}

func TestExecLineRoundTrip(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		j := testJob(t, goos)
		if _, err := Install(j); err != nil {
			t.Fatal(err)
		}
		args, err := ExecLine(goos, j.Home)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(args, []string{j.Bin, "sync", "--scheduled"}) {
			t.Errorf("%s: exec line %q", goos, args)
		}
	}
}

func TestInstallTakesOverHandMadeJob(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		j := testJob(t, goos)
		fs := Files(goos, j.Home)
		if err := os.MkdirAll(filepath.Dir(fs[0]), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fs[0], []byte("hand made\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// a sync never takes over a hand-made job; an explicit install does
		if goos == runtime.GOOS {
			if res, err := Apply(true, "gpu-box"); err != nil || len(res.Lines) > 0 {
				t.Fatalf("%s: apply touched a hand-made job: %v %v", goos, res, err)
			}
		}
		st := Check(goos, j.Home, j.StateDir)
		if !st.Installed || st.Ours {
			t.Fatalf("%s: status %+v", goos, st)
		}
		if s, _, _ := st.Problem(time.Now()); s != "warn" {
			t.Errorf("%s: a hand-made job should warn, got %s", goos, s)
		}
		res, err := Install(j)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(res.Lines, "\n"), "took over") {
			t.Errorf("%s: no takeover line: %v", goos, res.Lines)
		}
		backups, _ := filepath.Glob(filepath.Join(j.StateDir, "schedule", "backup", "*", filepath.Base(fs[0])))
		if len(backups) != 1 {
			t.Fatalf("%s: backups %v", goos, backups)
		}
		if b, _ := os.ReadFile(backups[0]); string(b) != "hand made\n" {
			t.Errorf("%s: backup holds %q", goos, b)
		}
		if st := Check(goos, j.Home, j.StateDir); !st.Ours || !st.BinOK {
			t.Errorf("%s: after install %+v", goos, st)
		}
		// a second install with nothing to change says nothing
		if res, _ := Install(j); len(res.Lines) > 0 {
			t.Errorf("%s: second install: %v", goos, res.Lines)
		}
	}
}

func TestRemoveAndDecline(t *testing.T) {
	j := testJob(t, "linux")
	if _, err := Install(j); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(j.OS, j.Home, j.StateDir, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range Files(j.OS, j.Home) {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s left behind", p)
		}
	}
	if !Declined(j.StateDir) {
		t.Fatal("decline not recorded")
	}
	st := Check(j.OS, j.Home, j.StateDir)
	if s, _, _ := st.Problem(time.Now()); s != "pass" {
		t.Errorf("declined schedule should pass, got %s", s)
	}
	// install clears the decline
	if _, err := Install(j); err != nil {
		t.Fatal(err)
	}
	if Declined(j.StateDir) {
		t.Error("install should clear the decline")
	}
	// a hand-made job is never removed
	if _, err := Remove(j.OS, j.Home, j.StateDir, false); err != nil {
		t.Fatal(err)
	}
	fs := Files(j.OS, j.Home)
	if err := os.WriteFile(fs[0], []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := Remove(j.OS, j.Home, j.StateDir, false)
	if _, err := os.Stat(fs[0]); err != nil || len(res.Warn) == 0 {
		t.Errorf("hand-made job removed or not named: %v %v", err, res)
	}
}

func TestProblems(t *testing.T) {
	now := time.Now()
	ok, bad := 0, 3
	fin := now.Add(-5 * time.Minute)
	base := Status{Files: []string{"/x"}, Installed: true, Ours: true, BinOK: true, Bin: "/b"}
	cases := []struct {
		name string
		s    Status
		want string
	}{
		{"missing", Status{Files: []string{"/x"}}, "fail"},
		{"foreign", Status{Files: []string{"/x"}, Installed: true}, "warn"},
		{"bin gone", Status{Files: []string{"/x"}, Installed: true, Ours: true, Bin: "/gone"}, "fail"},
		{"not loaded", func() Status { s := base; f := false; s.Loaded = &f; return s }(), "fail"},
		{"no run yet", base, "pass"},
		{"ok", func() Status { s := base; s.LastRun = &Run{Started: fin, Finished: &fin, Exit: &ok}; return s }(), "pass"},
		{"failed", func() Status { s := base; s.LastRun = &Run{Started: fin, Finished: &fin, Exit: &bad}; return s }(), "warn"},
		{"old", func() Status {
			s := base
			t := now.Add(-5 * time.Hour)
			s.LastRun = &Run{Started: t, Finished: &t, Exit: &ok}
			return s
		}(), "warn"},
		{"no linger", func() Status { s := base; f := false; s.Linger = &f; return s }(), "warn"},
	}
	for _, c := range cases {
		st, detail, fix := c.s.Problem(now)
		if st != c.want {
			t.Errorf("%s: got %s (%s)", c.name, st, detail)
		}
		if st != "pass" && fix == "" {
			t.Errorf("%s: no fix", c.name)
		}
	}
}

func TestRecordRun(t *testing.T) {
	dir := t.TempDir()
	RecordStart(dir)
	r, err := LastRun(dir)
	if err != nil || r == nil || r.Finished != nil {
		t.Fatalf("start: %+v %v", r, err)
	}
	RecordEnd(dir, 1, os.ErrNotExist)
	r, _ = LastRun(dir)
	if r.Finished == nil || r.Exit == nil || *r.Exit != 1 || r.Error == "" {
		t.Fatalf("end: %+v", r)
	}
}

// the service manager is called in the right order, and never under YONDER_HOME
func TestManagerCalls(t *testing.T) {
	j := testJob(t, "linux")
	var calls []string
	old := command
	command = func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "loginctl" {
			return "yes", nil
		}
		return "active", nil
	}
	oldNo := noManager
	noManager = func(string) bool { return false }
	defer func() { command, noManager = old, oldNo }()
	if _, err := Install(j); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("called the service manager under YONDER_HOME: %v", calls)
	}
	t.Setenv("YONDER_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	j.Self = "laptop" // a different file, so it reinstalls
	if _, err := Install(j); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"systemctl --user disable --now yonder-sync.timer", "systemctl --user daemon-reload", "systemctl --user enable --now yonder-sync.timer"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
}

func TestStableBin(t *testing.T) {
	if got := StableBin("/home/me/.local/bin/yonder"); got != "/home/me/.local/bin/yonder" {
		t.Errorf("got %s", got)
	}
	// no brew link on this box: keep what we have
	if got := StableBin("/nowhere/Cellar/yonder/0.4.0/bin/yonder"); got != "/nowhere/Cellar/yonder/0.4.0/bin/yonder" {
		t.Errorf("got %s", got)
	}
}

// the agents layer turning on or off, as a sync brings it, adds or removes the schedule
func TestApplyFollowsLayer(t *testing.T) {
	j := testJob(t, runtime.GOOS)
	if res, err := Apply(true, "gpu-box"); err != nil || len(res.Lines) == 0 {
		t.Fatalf("apply on: %v %v", res, err)
	}
	if st := Check(j.OS, j.Home, j.StateDir); !st.Installed || !st.Ours {
		t.Fatalf("not installed: %+v", st)
	}
	if res, _ := Apply(true, "gpu-box"); len(res.Lines) > 0 {
		t.Errorf("second apply changed something: %v", res.Lines)
	}
	if _, err := Apply(false, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	if st := Check(j.OS, j.Home, j.StateDir); st.Installed {
		t.Fatal("layer off left the schedule")
	}
	if Declined(j.StateDir) {
		t.Error("layer off is not a decline: turning it on again should reinstall")
	}
	if _, err := Remove(j.OS, j.Home, j.StateDir, true); err != nil {
		t.Fatal(err)
	}
	if res, _ := Apply(true, "gpu-box"); len(res.Lines) > 0 {
		t.Errorf("apply ignored the decline: %v", res.Lines)
	}
}

// linux without systemd: one plain warning, a quiet second install, a WARN in doctor
func TestNoSystemd(t *testing.T) {
	j := testJob(t, "linux")
	oldNo, oldCmd := noManager, command
	noManager = func(string) bool { return true }
	command = func(name string, args ...string) (string, error) {
		t.Fatalf("called %s with no systemd", name)
		return "", nil
	}
	defer func() { noManager, command = oldNo, oldCmd }()
	t.Setenv("YONDER_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	res, err := Install(j)
	if err != nil || len(res.Warn) != 1 || !strings.Contains(res.Warn[0], "no systemd") {
		t.Fatalf("first install: %+v %v", res, err)
	}
	if res, _ := Install(j); len(res.Warn)+len(res.Lines) != 0 {
		t.Fatalf("second install should be quiet: %+v", res)
	}
	st := Check(j.OS, j.Home, j.StateDir)
	if s, d, _ := st.Problem(time.Now()); s != "warn" || !strings.Contains(d, "another machine") {
		t.Fatalf("doctor: %s %s", s, d)
	}
}
