package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLaunchd is a launchctl with one job loaded under the label from loaded.
func fakeLaunchd(t *testing.T, loaded *string) {
	t.Helper()
	old, oldNo := command, noManager
	noManager = func(string) bool { return false }
	command = func(name string, args ...string) (string, error) {
		if name != "launchctl" {
			return "", os.ErrNotExist
		}
		switch args[0] {
		case "print":
			if *loaded == "" {
				return "Could not find service", os.ErrNotExist
			}
			return Label + " = {\n\tpath = " + *loaded + "\n\tstate = waiting\n}", nil
		case "bootout":
			*loaded = ""
		case "bootstrap":
			*loaded = args[2]
		}
		return "", nil
	}
	t.Cleanup(func() { command, noManager = old, oldNo })
}

// review item 9: a job loaded under the label from another plist is moved aside on takeover
func TestTakeoverMovesOtherPlistSameLabel(t *testing.T) {
	j := testJob(t, "darwin")
	other := filepath.Join(j.Home, "Library", "LaunchAgents", "me.handmade.yonder-sync.plist")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("<plist><dict><key>Label</key><string>"+Label+"</string></dict></plist>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded := other
	fakeLaunchd(t, &loaded)
	t.Setenv("YONDER_HOME", "") // talk to the fake launchd
	res, err := Install(j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Fatalf("the other plist with the label is still in LaunchAgents: %v", res.Lines)
	}
	backups, _ := filepath.Glob(filepath.Join(j.StateDir, "schedule", "backup", "*", filepath.Base(other)))
	if len(backups) != 1 {
		t.Fatalf("not backed up: %v", backups)
	}
	if loaded != Files("darwin", j.Home)[0] {
		t.Fatalf("launchd should now run yonder's plist, runs %q", loaded)
	}
}

// review item 9 (doctor side): the label loaded from another plist isn't yonder's job loaded
func TestCheckSeesLabelLoadedFromOtherPlist(t *testing.T) {
	j := testJob(t, "darwin")
	if _, err := Install(j); err != nil { // under YONDER_HOME: files only
		t.Fatal(err)
	}
	loaded := filepath.Join(j.Home, "Library", "LaunchAgents", "me.handmade.plist")
	fakeLaunchd(t, &loaded)
	t.Setenv("YONDER_HOME", "")
	st := Check("darwin", j.Home, j.StateDir)
	if st.Loaded == nil || *st.Loaded || !strings.Contains(st.LoadError, "me.handmade.plist") {
		t.Fatalf("status: loaded=%v err=%q", st.Loaded, st.LoadError)
	}
}

// review item 14: % is escaped in unit files, and a control character is refused
func TestUnitEscapesPercentAndRefusesNewline(t *testing.T) {
	j := testJob(t, "linux")
	j.Bin = "/home/me/50%off/yonder"
	j.Home = "/home/me/50%off"
	svc := j.Service()
	if !strings.Contains(svc, `ExecStart="/home/me/50%%off/yonder" sync --scheduled`) || !strings.Contains(svc, "WorkingDirectory=/home/me/50%%off\n") {
		t.Fatalf("unescaped %%:\n%s", svc)
	}
	k := testJob(t, "linux")
	if _, err := Install(k); err != nil {
		t.Fatal(err)
	}
	if args, err := ExecLine("linux", k.Home); err != nil || args[0] != k.Bin {
		t.Fatalf("exec line round trip: %v %v", args, err)
	}
	k.Env = map[string]string{"YONDER_SSH_IDENTITY": "/home/me/.ssh/id\nExecStartPre=/bin/touch /tmp/x"}
	if _, err := Install(k); err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("a newline in a carried variable must be refused: %v", err)
	}
}
