package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLocalEndToEnd drives `run` against this machine (the in-process path, no ssh).
func TestLocalEndToEnd(t *testing.T) {
	f := newFleet(t, "testdata/fleet.json")

	if r := f.run("", "ls"); r.code != 0 || !strings.Contains(r.stdout, "yonder init") {
		t.Fatalf("ls before init should point at init: %+v", r)
	}
	if r := f.run("", "run", "laptop", "--", "true"); r.code != 1 || !strings.Contains(r.stderr, "yonder init") {
		t.Fatalf("run before init should point at init: %+v", r)
	}
	r := f.must("init", "--yes")
	if !strings.Contains(r.stdout, "this machine is laptop") || !strings.Contains(r.stdout, "phone: iOS device, skipped") {
		t.Fatalf("init output: %s", r.stdout)
	}

	if r := f.must("version"); !strings.HasPrefix(r.stdout, "yonder "+testVersion) {
		t.Fatalf("version: %q", r.stdout)
	}

	if r := f.must("run", "--as", "codex", "laptop", "--", "echo hi"); r.stdout != "hi\n" {
		t.Fatalf("run: %+v", r)
	}
	if r := f.run("", "run", "laptop", "--", "echo", "a b", "$HOME"); r.stdout != "a b $HOME\n" {
		t.Fatalf("multi-arg quoting: %+v", r)
	}
	if r := f.run("", "run", "laptop", "--", "echo oops >&2; exit 3"); r.code != 3 || r.stderr != "oops\n" {
		t.Fatalf("exit code passthrough: %+v", r)
	}
	if r := f.run("piped input\n", "run", "laptop", "--", "cat"); r.stdout != "piped input\n" {
		t.Fatalf("stdin passthrough: %+v", r)
	}
	if r := f.run("", "run", "laptop", "--secret", "API_KEY", "--", "true"); r.code != 1 || !strings.Contains(r.stderr, "no secrets provider") {
		t.Fatalf("secret stub: %+v", r)
	}
	if r := f.run("", "run", "laptop"); r.code != 2 {
		t.Fatalf("usage error: %+v", r)
	}
	if r := f.run("", "run", "nope", "--", "true"); r.code != 1 || !strings.Contains(r.stderr, "known: laptop") {
		t.Fatalf("unknown machine: %+v", r)
	}

	// an offline peer: add saves it as pending, run refuses with 75 and the notes
	r = f.run("", "add", "gpu-box", "--yes", "--notes", "sleeps at night", "--role", "gpu")
	if r.code != 75 || !strings.Contains(r.stderr, "saved as pending") {
		t.Fatalf("add offline: %+v", r)
	}
	r = f.run("", "run", "gpu-box", "--", "nvidia-smi")
	if r.code != 75 || !strings.Contains(r.stderr, "gpu-box is offline (last seen ") || !strings.Contains(r.stderr, "— notes: sleeps at night") {
		t.Fatalf("offline run: %+v", r)
	}

	// detached run, then its job list and log
	r = f.must("run", "--detach", "laptop", "--", "echo started; exit 4")
	id := strings.TrimSpace(r.stdout)
	if id == "" {
		t.Fatal("detach should print a job id")
	}
	var jobs []struct {
		ID, State string
		Exit      *int
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r = f.must("jobs", "laptop", "--json")
		if err := json.Unmarshal([]byte(r.stdout), &jobs); err != nil {
			t.Fatal(err, r.stdout)
		}
		if len(jobs) == 1 && jobs[0].State == "exited" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(jobs) != 1 || jobs[0].ID != id || jobs[0].Exit == nil || *jobs[0].Exit != 4 {
		t.Fatalf("jobs: %+v", jobs)
	}
	if r := f.must("jobs", "laptop", id); r.stdout != "started\n" {
		t.Fatalf("job log: %q", r.stdout)
	}

	// ls --json: stable shape agents parse
	var ls struct {
		Self     string `json:"self"`
		Machines []struct {
			Name     string     `json:"name"`
			State    string     `json:"state"`
			Online   *bool      `json:"online"`
			LastSeen *time.Time `json:"last_seen"`
			Role     string     `json:"role"`
			Notes    string     `json:"notes"`
		} `json:"machines"`
	}
	r = f.must("ls", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &ls); err != nil {
		t.Fatal(err)
	}
	if ls.Self != "laptop" || len(ls.Machines) != 2 {
		t.Fatalf("ls: %+v", ls)
	}
	gpu, self := ls.Machines[0], ls.Machines[1]
	if gpu.Name != "gpu-box" || gpu.State != "pending" || gpu.Online == nil || *gpu.Online || gpu.LastSeen == nil || gpu.Role != "gpu" {
		t.Fatalf("gpu-box: %+v", gpu)
	}
	if self.Name != "laptop" || self.State != "self" {
		t.Fatalf("self: %+v", self)
	}
	if r := f.must("ls"); !strings.Contains(r.stdout, "this machine") || !strings.Contains(r.stdout, "pending, offline") {
		t.Fatalf("ls table: %s", r.stdout)
	}
	if r := f.must("show", "gpu-box", "--json"); !strings.Contains(r.stdout, `"record"`) {
		t.Fatalf("show --json should carry the record: %s", r.stdout)
	}

	// the log merges target-side runs and the caller-side failure
	var entries []struct {
		To, Harness, Side, Command string
		Exit                       *int
	}
	r = f.must("log", "--json", "--limit", "0")
	if err := json.Unmarshal([]byte(r.stdout), &entries); err != nil {
		t.Fatal(err)
	}
	var sawCodex, sawCaller, sawExit3 bool
	for _, e := range entries {
		sawCodex = sawCodex || (e.Harness == "codex" && e.Command == "echo hi")
		sawCaller = sawCaller || (e.Side == "caller" && e.To == "gpu-box" && e.Exit != nil && *e.Exit == 75)
		sawExit3 = sawExit3 || (e.Exit != nil && *e.Exit == 3)
	}
	if !sawCodex || !sawCaller || !sawExit3 {
		t.Fatalf("log entries missing (codex %v caller %v exit3 %v): %s", sawCodex, sawCaller, sawExit3, r.stdout)
	}
	if r := f.must("log", "--machine", "gpu-box", "--json"); strings.Count(r.stdout, `"id"`) != 1 {
		t.Fatalf("log --machine: %s", r.stdout)
	}

	// refresh with the only peer offline still refreshes self
	if r := f.must("refresh"); !strings.Contains(r.stdout, "refreshed laptop") || !strings.Contains(r.stdout, "skipped gpu-box") {
		t.Fatalf("refresh: %+v", r)
	}

	// doctor: an offline pending peer is a warning, not a failure
	if r := f.run("", "doctor"); r.code != 0 || !strings.Contains(r.stdout, "WARN  online") || !strings.Contains(r.stdout, "Nothing broken") {
		t.Fatalf("doctor: %+v", r)
	}
}

func TestInitNoSkillLeavesHarnessesAlone(t *testing.T) {
	skill := func(f *fleet) bool {
		_, err := os.Stat(filepath.Join(f.home, "yonder", "home", ".claude", "skills", "yonder"))
		return err == nil
	}
	with := newFleet(t, "testdata/fleet.json")
	if err := os.MkdirAll(filepath.Join(with.home, "yonder", "home", ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	with.must("init", "--yes")
	if !skill(with) {
		t.Fatal("control: init should install the skill for a detected harness")
	}
	without := newFleet(t, "testdata/fleet.json")
	if err := os.MkdirAll(filepath.Join(without.home, "yonder", "home", ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	without.must("init", "--yes", "--no-skill")
	if skill(without) {
		t.Fatal("--no-skill still installed the skill")
	}
}

// TestCopyEndToEnd copies through this machine's own in-process path, and
// refuses an offline peer with 75.
func TestCopyEndToEnd(t *testing.T) {
	f := newFleet(t, "testdata/fleet.json")
	f.must("init", "--yes", "--no-skill")
	src := filepath.Join(f.home, "Talk.key")
	if err := os.MkdirAll(filepath.Join(src, "Data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "Data", "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ~ on the receiving side expands there; under YONDER_HOME that is $YONDER_HOME/home
	r := f.must("cp", src, "laptop:~/inbox/")
	if !strings.HasPrefix(r.stdout, "copied Talk.key (folder, 1 file, 5 B) to laptop:") {
		t.Fatalf("cp: %+v", r)
	}
	if b, err := os.ReadFile(filepath.Join(f.home, "yonder", "home", "inbox", "Talk.key", "Data", "a.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("copied file: %q %v", b, err)
	}
	if r := f.run("", "cp", src, "laptop:~/inbox/"); r.code != 1 || !strings.Contains(r.stderr, "already exists; add --force") {
		t.Fatalf("no-overwrite: %+v", r)
	}
	if r := f.must("log"); !strings.Contains(r.stdout, "(1 file, 5 B)") {
		t.Fatalf("log: %s", r.stdout)
	}
	f.run("", "add", "gpu-box", "--yes")
	if r := f.run("", "cp", src, "gpu-box:~/"); r.code != 75 || !strings.Contains(r.stderr, "gpu-box is offline") {
		t.Fatalf("offline cp: %+v", r)
	}
}
