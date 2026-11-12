package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/runlog"
	"github.com/zacharytgray/yonder/internal/transport"
)

// the detach test re-runs this test binary as `_job`
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_job" {
		os.Exit(Job(context.Background(), nil, os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func setup(t *testing.T) {
	t.Helper()
	t.Setenv("YONDER_HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("SSH_CONNECTION", "") // the in-process path unless a test sets it
	if err := registry.Open().SetSelf("gpu-box"); err != nil {
		t.Fatal(err)
	}
}

func input(t *testing.T, env Envelope, rest string) io.Reader {
	t.Helper()
	line, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return io.MultiReader(bytes.NewReader(line), strings.NewReader(rest))
}

func TestEnvelopeRoundTripAndPassthrough(t *testing.T) {
	env := Envelope{ID: "id-1", Command: "cat", From: "laptop", Harness: "codex", Secrets: map[string]string{"B": "2", "A": "1"}}
	br := bufio.NewReader(input(t, env, "line one\nline two\n"))
	got, err := ReadEnvelope(br)
	if err != nil {
		t.Fatal(err)
	}
	if got.V != envelopeVersion || got.Command != "cat" || got.Secrets["A"] != "1" {
		t.Fatalf("got %+v", got)
	}
	if names := got.SecretNames(); names[0] != "A" || names[1] != "B" {
		t.Fatalf("names should sort: %v", names)
	}
	rest, _ := io.ReadAll(br)
	if string(rest) != "line one\nline two\n" {
		t.Fatalf("stdin passthrough: %q", rest)
	}
}

func TestEnvelopeErrors(t *testing.T) {
	// a missing, zero or negative v fails closed
	for _, in := range []string{"", "not json\n", `{"v":1}` + "\n", `{"v":99,"command":"x"}` + "\n",
		`{"command":"x"}` + "\n", `{"v":0,"command":"x"}` + "\n", `{"v":-1,"command":"x"}` + "\n"} {
		if _, err := ReadEnvelope(bufio.NewReader(strings.NewReader(in))); err == nil {
			t.Errorf("%q should fail", in)
		}
	}
	// no trailing newline is fine
	if _, err := ReadEnvelope(bufio.NewReader(strings.NewReader(`{"v":1,"command":"x"}`))); err != nil {
		t.Fatal(err)
	}
}

func TestExecScrubsAndLogs(t *testing.T) {
	setup(t)
	env := Envelope{
		ID: "run-1", Command: `echo "out $API_KEY"; echo "err $API_KEY" >&2; cat; exit 7`,
		From: "laptop", Harness: "claude-code", Secrets: map[string]string{"API_KEY": "sk-example-123"},
	}
	var out, errb bytes.Buffer
	code := Exec(context.Background(), nil, input(t, env, "from stdin\n"), &out, &errb)
	if code != 7 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if out.String() != "out [API_KEY]\nfrom stdin\n" {
		t.Fatalf("stdout %q", out.String())
	}
	if errb.String() != "err [API_KEY]\n" {
		t.Fatalf("stderr %q", errb.String())
	}
	es, err := runlog.Read(registry.LogPath(), time.Time{})
	if err != nil || len(es) != 2 {
		t.Fatalf("log: %v %+v", err, es)
	}
	if s := es[0]; s.Status != runlog.StatusStarted || s.Exit != nil || s.ID != "run-1" {
		t.Fatalf("start entry %+v", s)
	}
	e := es[1]
	if e.ID != "run-1" || e.From != "laptop" || e.To != "gpu-box" || *e.Exit != 7 || e.Side != runlog.SideTarget || e.Secrets[0] != "API_KEY" || e.Status != "" {
		t.Fatalf("entry %+v", e)
	}
	if m := runlog.Merge(es); len(m) != 1 || m[0].Exit == nil {
		t.Fatalf("merged %+v", m)
	}
	raw, _ := os.ReadFile(registry.LogPath())
	if bytes.Contains(raw, []byte("sk-example-123")) {
		t.Fatal("secret value leaked into the log")
	}
}

// a run cut off midway (caller gone, ssh dropped) must already be in the log
func TestExecLogsStartBeforeRunning(t *testing.T) {
	setup(t)
	env := Envelope{
		ID: "run-2", From: "laptop", Secrets: map[string]string{"TOK": "sk-start-secret-9"},
		Command: `echo sk-start-secret-9 >/dev/null; cat "` + registry.LogPath() + `"`,
	}
	var out bytes.Buffer
	if code := Exec(context.Background(), nil, input(t, env, ""), &out, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var e runlog.Entry
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &e); err != nil {
		t.Fatalf("command saw no single start entry: %q (%v)", out.String(), err)
	}
	if e.ID != "run-2" || e.Status != runlog.StatusStarted || e.Exit != nil || e.Secrets[0] != "TOK" {
		t.Fatalf("start entry %+v", e)
	}
	if strings.Contains(e.Command, "sk-start-secret-9") {
		t.Fatalf("start entry not scrubbed: %q", e.Command)
	}
}

func TestExecFallsBackToSh(t *testing.T) {
	setup(t)
	t.Setenv("SHELL", "/nonexistent/shell")
	var out bytes.Buffer
	if code := Exec(context.Background(), nil, input(t, Envelope{Command: "echo ok"}, ""), &out, io.Discard); code != 0 || out.String() != "ok\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestDetach(t *testing.T) {
	setup(t)
	env := Envelope{ID: "job-1", Command: `echo "tok=$T"; exit 3`, From: "laptop", Detach: true, Secrets: map[string]string{"T": "hidden-value"}}
	var out bytes.Buffer
	if code := Exec(context.Background(), nil, input(t, env, ""), &out, os.Stderr); code != 0 {
		t.Fatalf("detach exit %d", code)
	}
	if strings.TrimSpace(out.String()) != "job-1" {
		t.Fatalf("want job id, got %q", out.String())
	}
	var st []JobStatus
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		if st, err = ListJobs(registry.JobsDir()); err != nil {
			t.Fatal(err)
		}
		if len(st) == 1 && st[0].State == "exited" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(st) != 1 || st[0].State != "exited" || *st[0].Exit != 3 {
		t.Fatalf("job status %+v", st)
	}
	var logOut bytes.Buffer
	if code := Jobs(context.Background(), []string{"job-1"}, nil, &logOut, io.Discard); code != 0 {
		t.Fatal("jobs <id>")
	}
	if logOut.String() != "tok=[T]\n" {
		t.Fatalf("job log %q", logOut.String())
	}
	if code := Jobs(context.Background(), []string{"../x"}, nil, io.Discard, io.Discard); code == 0 {
		t.Fatal("bad id must be refused")
	}
}

// startJob detaches command as job id and waits until its command is running.
func startJob(t *testing.T, id, command string) {
	t.Helper()
	env := Envelope{ID: id, Command: command, From: "laptop", Detach: true}
	if code := Exec(context.Background(), nil, input(t, env, ""), io.Discard, os.Stderr); code != 0 {
		t.Fatalf("detach exit %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, err := loadStatus(registry.JobsDir(), id)
		log, _ := os.ReadFile(filepath.Join(registry.JobsDir(), id+".log"))
		if err == nil && st.PGID > 0 && strings.Contains(string(log), "up") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never started", id)
}

func TestFollowAndKill(t *testing.T) {
	setup(t)
	jobPoll = 20 * time.Millisecond
	killGrace = 500 * time.Millisecond
	ctx := context.Background()
	killer := func(id string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Kill(ctx, []string{id}, input(t, Envelope{Command: "kill job " + id, From: "laptop", Harness: "codex"}, ""), &out, &errb)
		return code, out.String(), errb.String()
	}

	// follow streams to the end and exits with the job's code
	startJob(t, "job-f", `echo up; sleep 0.3; echo two; exit 5`)
	var out bytes.Buffer
	if code := Jobs(ctx, []string{"job-f", "--follow"}, nil, &out, os.Stderr); code != 5 || out.String() != "up\ntwo\n" {
		t.Fatalf("follow: code %d out %q", code, out.String())
	}
	if code, _, errs := killer("job-f"); code != 1 || !strings.Contains(errs, "not running (exited 5)") {
		t.Fatalf("kill of a finished job: %d %q", code, errs)
	}

	// sigterm is enough for a plain command
	startJob(t, "job-t", `echo up; sleep 30`)
	if code, msg, errs := killer("job-t"); code != 0 || !strings.Contains(msg, "with SIGTERM (exit 143)") {
		t.Fatalf("kill: %d %q %q", code, msg, errs)
	}
	if code := Jobs(ctx, []string{"job-t", "--follow"}, nil, io.Discard, io.Discard); code != 143 {
		t.Fatalf("follow after kill: %d", code)
	}

	// a command that ignores sigterm gets sigkill after the grace period
	startJob(t, "job-k", `trap "" TERM; echo up; sleep 30`)
	if code, msg, errs := killer("job-k"); code != 0 || !strings.Contains(msg, "with SIGKILL after") || !strings.Contains(msg, "(exit 137)") {
		t.Fatalf("kill -9: %d %q %q", code, msg, errs)
	}
	st, err := loadStatus(registry.JobsDir(), "job-k")
	if err != nil || st.State != "exited" || *st.Exit != 137 {
		t.Fatalf("status after kill: %+v %v", st, err)
	}

	if code, _, _ := killer("no-such-job"); code != 1 {
		t.Fatal("kill of a missing job must fail")
	}
	if code := Jobs(ctx, []string{"--follow"}, nil, io.Discard, io.Discard); code != 2 {
		t.Fatal("--follow without an id is a usage error")
	}

	// every kill is logged here, with who asked
	es, err := runlog.Read(registry.LogPath(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var kills []runlog.Entry
	for _, e := range es {
		if strings.HasPrefix(e.Command, "kill job ") {
			kills = append(kills, e)
		}
	}
	if len(kills) != 4 || *kills[0].Exit != 1 || kills[0].Error == "" {
		t.Fatalf("want 4 kill entries, got %+v", kills)
	}
	k := kills[1]
	if k.Command != "kill job job-t: echo up; sleep 30" || k.From != "laptop" || k.Harness != "codex" || *k.Exit != 0 || k.Side != runlog.SideTarget {
		t.Fatalf("kill entry %+v", k)
	}
}

func TestSync(t *testing.T) {
	setup(t)
	t0 := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	s := registry.Open()
	if err := s.SaveMachine(registry.Machine{Name: "gpu-box", UpdatedAt: t0, Notes: "local"}); err != nil {
		t.Fatal(err)
	}
	in := registry.Bundle{Machines: []registry.Machine{
		{Name: "laptop", UpdatedAt: t0},
		{Name: "gpu-box", UpdatedAt: t0.Add(-time.Hour), Notes: "stale"},
	}}
	data, _ := json.Marshal(in)
	var out bytes.Buffer
	if code := Sync(context.Background(), nil, bytes.NewReader(data), &out, os.Stderr); code != 0 {
		t.Fatal("sync failed")
	}
	var got registry.Bundle
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Machines) != 2 || got.Machines[0].Notes != "local" {
		t.Fatalf("merged %+v", got)
	}
	if _, err := s.Machine("laptop"); err != nil {
		t.Fatal("incoming record should be saved")
	}
}

func TestLogSince(t *testing.T) {
	setup(t)
	t0 := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	for i := range 3 {
		_ = runlog.Append(registry.LogPath(), runlog.Entry{Time: t0.Add(time.Duration(i) * time.Hour), ID: string(rune('a' + i))})
	}
	var out bytes.Buffer
	if code := Log(context.Background(), []string{"--since", t0.Add(time.Hour).Format(time.RFC3339)}, nil, &out, io.Discard); code != 0 {
		t.Fatal("log failed")
	}
	if n := strings.Count(out.String(), "\n"); n != 2 {
		t.Fatalf("want 2 lines, got %d: %s", n, out.String())
	}
	if code := Log(context.Background(), []string{"--bogus"}, nil, io.Discard, io.Discard); code != 2 {
		t.Fatal("unknown flag is a usage error")
	}
}

func TestFactsAndInitSelf(t *testing.T) {
	setup(t)
	var out bytes.Buffer
	if code := FactsCmd(context.Background(), nil, nil, &out, io.Discard); code != 0 {
		t.Fatal("facts")
	}
	var f registry.Facts
	if err := json.Unmarshal(out.Bytes(), &f); err != nil || f.OS == "" || f.CPUCores == 0 {
		t.Fatalf("facts %v %+v", err, f)
	}
	// it reports the key ssh offers by default, for mesh to grant
	key := filepath.Join(registry.UserHome(), ".ssh", "id_ed25519")
	_ = os.MkdirAll(filepath.Dir(key), 0o700)
	_ = os.WriteFile(key, []byte("private"), 0o600)
	_ = os.WriteFile(key+".pub", []byte("ssh-ed25519 AAAAdesk me@desk\n"), 0o600)
	out.Reset()
	if code := InitSelf(context.Background(), []string{"desk"}, nil, &out, io.Discard); code != 0 {
		t.Fatal("init-self")
	}
	if got := strings.TrimSpace(out.String()); got != `{"mesh_keys":["ssh-ed25519 AAAAdesk me@desk"]}` {
		t.Fatalf("init-self printed %s", got)
	}
	if c, _ := registry.Open().Config(); c.Self != "desk" {
		t.Fatalf("self %q", c.Self)
	}
	if code := Dispatch(context.Background(), []string{"_nope"}, nil, io.Discard, io.Discard); code != 2 {
		t.Fatal("unknown hidden command")
	}
}

func TestSyncStripsLostKeysOnce(t *testing.T) {
	setup(t)
	const lost = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlostkey me@old-server"
	const other = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIotherkey me@laptop"
	ak := transport.AuthorizedKeysPath(registry.UserHome())
	if err := os.MkdirAll(filepath.Dir(ak), 0o700); err != nil {
		t.Fatal(err)
	}
	orig := other + "\n" + lost + "\n# keep me\n"
	if err := os.WriteFile(ak, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	// this machine held old-server's record, so it may strip those keys
	if err := registry.Open().SaveMachine(registry.Machine{Name: "old-server", AddedAt: t0.Add(-time.Hour), UpdatedAt: t0.Add(-time.Hour), SSHClientKeys: []string{lost}}); err != nil {
		t.Fatal(err)
	}
	tomb := registry.Tombstone{Name: "old-server", RemovedAt: t0, Lost: true, SSHClientKeys: []string{lost}}
	sync := func(b registry.Bundle) {
		t.Helper()
		data, _ := json.Marshal(b)
		if code := Sync(context.Background(), nil, bytes.NewReader(data), io.Discard, os.Stderr); code != 0 {
			t.Fatal("sync failed")
		}
	}

	sync(registry.Bundle{Tombstones: []registry.Tombstone{tomb}})
	if b, _ := os.ReadFile(ak); string(b) != other+"\n# keep me\n" {
		t.Fatalf("authorized_keys: %q", b)
	}
	if baks, _ := filepath.Glob(ak + ".yonder-bak.*"); len(baks) != 1 {
		t.Fatalf("backups: %v", baks)
	} else if b, _ := os.ReadFile(baks[0]); string(b) != orig {
		t.Fatalf("backup: %q", b)
	}

	// applied once: a key added back by hand survives later syncs
	if err := os.WriteFile(ak, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	sync(registry.Bundle{})
	sync(registry.Bundle{Tombstones: []registry.Tombstone{tomb}})
	if b, _ := os.ReadFile(ak); string(b) != orig {
		t.Fatalf("stripped again: %q", b)
	}
}

// remove --lost --include-shared reaches a machine that was offline: the
// tombstone carries include_shared, and an upgrade of one already applied
// strips the shared key at the next sync
func TestSyncHonoursIncludeShared(t *testing.T) {
	setup(t)
	const own = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIownkey me@old-server"
	const shared = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIsharedkey me@desk"
	ak := transport.AuthorizedKeysPath(registry.UserHome())
	if err := os.MkdirAll(filepath.Dir(ak), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ak, []byte(own+"\n"+shared+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	s := registry.Open()
	for _, m := range []registry.Machine{
		{Name: "old-server", AddedAt: t0.Add(-time.Hour), UpdatedAt: t0.Add(-time.Hour), SSHClientKeys: []string{own, shared}},
		{Name: "desk", AddedAt: t0.Add(-time.Hour), UpdatedAt: t0.Add(-time.Hour), SSHClientKeys: []string{shared}},
	} {
		if err := s.SaveMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	sync := func(tomb registry.Tombstone) {
		t.Helper()
		data, _ := json.Marshal(registry.Bundle{Tombstones: []registry.Tombstone{tomb}})
		if code := Sync(context.Background(), nil, bytes.NewReader(data), io.Discard, os.Stderr); code != 0 {
			t.Fatal("sync failed")
		}
	}
	plain := registry.Tombstone{Name: "old-server", RemovedAt: t0, Lost: true, SSHClientKeys: []string{own, shared}}
	sync(plain)
	if b, _ := os.ReadFile(ak); string(b) != shared+"\n" {
		t.Fatalf("plain lost tombstone keeps the shared key: %q", b)
	}
	up := plain
	up.IncludeShared = true
	sync(up)
	if b, _ := os.ReadFile(ak); string(b) != "" {
		t.Fatalf("include_shared should strip the shared key too: %q", b)
	}
	if tomb, _ := s.Tombstone("old-server"); !tomb.IncludeShared {
		t.Fatalf("stored tombstone %+v", tomb)
	}
	// a plain copy from a slower peer doesn't downgrade it
	sync(plain)
	if tomb, _ := s.Tombstone("old-server"); !tomb.IncludeShared {
		t.Fatalf("downgraded: %+v", tomb)
	}
}

func TestSyncStripsHeldLostTombstone(t *testing.T) {
	setup(t)
	const lost = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlostkey me@old-server"
	ak := transport.AuthorizedKeysPath(registry.UserHome())
	if err := os.MkdirAll(filepath.Dir(ak), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ak, []byte(lost+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// held from before, never applied
	t0 := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	s := registry.Open()
	if err := s.SaveTombstone(registry.Tombstone{Name: "old-server", RemovedAt: t0, Lost: true, SSHClientKeys: []string{lost}, HeldKeys: []string{lost}}); err != nil {
		t.Fatal(err)
	}
	if code := Sync(context.Background(), nil, strings.NewReader(`{}`), io.Discard, os.Stderr); code != 0 {
		t.Fatal("sync failed")
	}
	if b, _ := os.ReadFile(ak); string(b) != "" {
		t.Fatalf("authorized_keys: %q", b)
	}
}

func TestStripLostSkipsPlainTombstones(t *testing.T) {
	setup(t)
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIkey me@desk"
	ak := transport.AuthorizedKeysPath(registry.UserHome())
	if err := os.MkdirAll(filepath.Dir(ak), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ak, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := StripLost([]registry.Tombstone{{Name: "desk", RemovedAt: time.Now(), SSHClientKeys: []string{key}, HeldKeys: []string{key}}}, nil)
	if err != nil || len(lines) != 0 {
		t.Fatalf("%v %v", lines, err)
	}
	if b, _ := os.ReadFile(ak); string(b) != key+"\n" {
		t.Fatalf("a graceful removal must not strip keys: %q", b)
	}
}

// a tailnet where laptop is a fleet machine, old-server was removed and stranger never joined
const syncStatus = `{"BackendState":"Running",
 "Self":{"HostName":"gpu-box","DNSName":"gpu-box.example.ts.net.","TailscaleIPs":["100.64.0.2"],"UserID":1001},
 "Peer":{
  "a":{"HostName":"laptop","DNSName":"laptop.example.ts.net.","TailscaleIPs":["100.64.0.1","fd7a:115c:a1e0::1"],"Online":true,"UserID":1001},
  "b":{"HostName":"old-server","DNSName":"old-server.example.ts.net.","TailscaleIPs":["100.64.0.5"],"Online":true,"UserID":1001},
  "c":{"HostName":"stranger","DNSName":"stranger.example.ts.net.","TailscaleIPs":["100.64.0.7"],"Online":true,"UserID":1001}}}`

func TestSyncIdentifiesSender(t *testing.T) {
	setup(t)
	fixture := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(fixture, []byte(syncStatus), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YONDER_TAILSCALE_JSON", fixture)
	t0 := time.Now().UTC().Add(-time.Hour)
	s := registry.Open()
	for _, m := range []registry.Machine{
		{Name: "gpu-box", TailscaleIP: "100.64.0.2", UpdatedAt: t0, AddedAt: t0},
		{Name: "laptop", TailscaleIP: "100.64.0.1", UpdatedAt: t0, AddedAt: t0},
	} {
		if err := s.SaveMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveTombstone(registry.Tombstone{Name: "old-server", RemovedAt: t0}); err != nil {
		t.Fatal(err)
	}
	in := `{"machines":[{"name":"desk","updated_at":"` + t0.Format(time.RFC3339) + `","facts":{}}]}`
	sync := func(from string) (int, string) {
		t.Setenv("SSH_CONNECTION", from+" 50000 100.64.0.2 22")
		var errb bytes.Buffer
		code := Sync(context.Background(), nil, strings.NewReader(in), io.Discard, &errb)
		return code, errb.String()
	}
	for from, want := range map[string]string{
		"100.64.0.7":  "stranger (100.64.0.7) is not in this machine's registry",
		"100.64.0.5":  "old-server (100.64.0.5) was removed from the fleet",
		"192.168.1.9": "192.168.1.9 is not a peer in this tailnet",
	} {
		if code, msg := sync(from); code == 0 || !strings.Contains(msg, "refused: "+want) {
			t.Fatalf("sync from %s: exit %d %q", from, code, msg)
		}
		if _, err := s.Machine("desk"); err == nil {
			t.Fatalf("refused sync from %s still wrote a record", from)
		}
	}
	// a fleet machine, by its v4 or v6 address
	for _, from := range []string{"100.64.0.1", "fd7a:115c:a1e0::1"} {
		if code, msg := sync(from); code != 0 {
			t.Fatalf("sync from laptop at %s: %d %s", from, code, msg)
		}
	}
	if _, err := s.Machine("desk"); err != nil {
		t.Fatal("sync from laptop should be merged")
	}
	if name, err := Sender(context.Background(), s, "100.64.0.1 50000 100.64.0.2 22"); err != nil || name != "laptop" {
		t.Fatalf("sender %q %v", name, err)
	}
}

func TestSyncFirstFromEmptyRegistry(t *testing.T) {
	setup(t)
	t.Setenv("YONDER_TAILSCALE_JSON", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("SSH_CONNECTION", "100.64.0.1 50000 100.64.0.2 22")
	now := time.Now().UTC().Format(time.RFC3339)
	in := `{"machines":[{"name":"gpu-box","updated_at":"` + now + `","facts":{}},{"name":"laptop","updated_at":"` + now + `","facts":{}}]}`
	if code := Sync(context.Background(), nil, strings.NewReader(in), io.Discard, os.Stderr); code != 0 {
		t.Fatal("a just-added machine should take its first sync")
	}
	if ms, _ := registry.Open().Machines(); len(ms) != 2 {
		t.Fatalf("got %+v", ms)
	}
}

func TestLostKeysOnlyHeldAndNotLive(t *testing.T) {
	const k1, k2, k3 = "ssh-ed25519 AAAAone me@old", "ssh-ed25519 AAAAtwo me@old", "ssh-ed25519 AAAAthree me@old"
	tomb := registry.Tombstone{Name: "old-server", Lost: true, SSHClientKeys: []string{k1, k2, k3}, HeldKeys: []string{"ssh-ed25519 AAAAone", k2}}
	live := []registry.Machine{{Name: "laptop", SSHClientKeys: []string{"restrict " + k2}}}
	if got := LostKeys(tomb, live); len(got) != 1 || got[0] != k1 {
		t.Fatalf("got %q, want only %q", got, k1)
	}
	tomb.HeldKeys = nil
	if got := LostKeys(tomb, nil); len(got) != 0 {
		t.Fatalf("never held a record: should strip nothing, got %q", got)
	}
}

func TestSyncNeverHeldStripsNothing(t *testing.T) {
	setup(t)
	const victim = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIvictim me@laptop"
	ak := transport.AuthorizedKeysPath(registry.UserHome())
	if err := os.MkdirAll(filepath.Dir(ak), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ak, []byte(victim+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// a peer claims a machine this one never knew was lost, with someone else's key
	b := registry.Bundle{Tombstones: []registry.Tombstone{{Name: "ghost", RemovedAt: time.Now().UTC(), Lost: true, SSHClientKeys: []string{victim}}}}
	data, _ := json.Marshal(b)
	if code := Sync(context.Background(), nil, bytes.NewReader(data), io.Discard, os.Stderr); code != 0 {
		t.Fatal("sync failed")
	}
	if got, _ := os.ReadFile(ak); string(got) != victim+"\n" {
		t.Fatalf("stripped a key this machine never held: %q", got)
	}
}

func TestLogAndJobScrubCommand(t *testing.T) {
	setup(t)
	sec := map[string]string{"API_KEY": "sk-example-123"}
	env := Envelope{ID: "run-2", Command: "echo sk-example-123", Secrets: sec}
	if code := Exec(context.Background(), nil, input(t, env, ""), io.Discard, io.Discard); code != 0 {
		t.Fatal("exec failed")
	}
	env = Envelope{ID: "job-2", Command: "echo sk-example-123", Secrets: sec, Detach: true}
	if code := Exec(context.Background(), nil, input(t, env, ""), io.Discard, os.Stderr); code != 0 {
		t.Fatal("detach failed")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, _ := ListJobs(registry.JobsDir())
		if len(st) == 1 && st[0].State == "exited" {
			if st[0].Command != "echo [API_KEY]" {
				t.Fatalf("job command %q", st[0].Command)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job never finished")
		}
		time.Sleep(50 * time.Millisecond)
	}
	raw, _ := os.ReadFile(registry.LogPath())
	job, _ := os.ReadFile(filepath.Join(registry.JobsDir(), "job-2.json"))
	if bytes.Contains(raw, []byte("sk-example-123")) || bytes.Contains(job, []byte("sk-example-123")) {
		t.Fatalf("secret value in log or job status:\n%s\n%s", raw, job)
	}
	if !bytes.Contains(raw, []byte(`"command":"echo [API_KEY]"`)) {
		t.Fatalf("log: %s", raw)
	}
}
