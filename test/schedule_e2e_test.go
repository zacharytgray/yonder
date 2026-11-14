package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSchedule: init with the agents layer installs the schedule, a run
// records itself for doctor, disable takes it out, --no-schedule keeps it off.
func TestSchedule(t *testing.T) {
	f := newFleet(t, "testdata/fleet.json")
	f.env = append(f.env, "YONDER_SCHEDULE_JITTER=off")
	f.must("init", "--yes", "--no-skill", "--layers", "agents")
	home := filepath.Join(f.home, "yonder", "home")
	job := filepath.Join(home, ".config", "systemd", "user", "yonder-sync.timer")
	if runtime.GOOS == "darwin" {
		job = filepath.Join(home, "Library", "LaunchAgents", "com.yonder.sync.plist")
	}
	if b, err := os.ReadFile(job); err != nil || !strings.Contains(string(b), "yonder:schedule v1") {
		t.Fatalf("no schedule after init: %v %s", err, b)
	}
	var st struct {
		Installed bool `json:"installed"`
		Ours      bool `json:"ours"`
		BinOK     bool `json:"bin_ok"`
		LastRun   *struct {
			Exit *int `json:"exit"`
		} `json:"last_run"`
	}
	if err := json.Unmarshal([]byte(f.must("schedule", "status", "--json").stdout), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Ours || !st.BinOK || st.LastRun != nil {
		t.Fatalf("status: %+v", st)
	}
	// what the timer runs
	r := f.must("sync", "--scheduled")
	if !strings.Contains(r.stdout, "scheduled sync") {
		t.Fatalf("scheduled run: %s", r.stdout)
	}
	_ = json.Unmarshal([]byte(f.must("schedule", "status", "--json").stdout), &st)
	if st.LastRun == nil || st.LastRun.Exit == nil || *st.LastRun.Exit != 0 {
		t.Fatalf("run not recorded: %+v", st.LastRun)
	}
	if r := f.run("", "doctor"); !strings.Contains(r.stdout, "every 15 minutes; last run") {
		t.Fatalf("doctor: %s", r.stdout)
	}

	f.must("disable", "agents")
	if _, err := os.Stat(job); err == nil {
		t.Fatal("disable agents left the schedule")
	}
	r = f.must("enable", "agents")
	if _, err := os.Stat(job); err != nil {
		t.Fatalf("enable agents didn't install it: %s", r.stdout)
	}

	g := newFleet(t, "testdata/fleet.json")
	g.must("init", "--yes", "--no-skill", "--layers", "agents", "--no-schedule")
	gjob := strings.Replace(job, f.home, g.home, 1)
	if _, err := os.Stat(gjob); err == nil {
		t.Fatal("--no-schedule installed one")
	}
	if r := g.run("", "doctor"); !strings.Contains(r.stdout, "off here") {
		t.Fatalf("doctor after --no-schedule: %s", r.stdout)
	}
	// a sync doesn't put a declined schedule back
	g.must("sync")
	if _, err := os.Stat(gjob); err == nil {
		t.Fatal("sync put back a declined schedule")
	}
}
