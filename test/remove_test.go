package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	lostKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlostkey me@old-server"
	otherKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIotherkey me@laptop"
)

// a fleet whose PATH has no harness binaries, so detection is up to the test
func newBareFleet(t *testing.T) *fleet {
	f := newFleet(t, "testdata/fleet.json")
	for i, kv := range f.env {
		if strings.HasPrefix(kv, "PATH=") {
			f.env[i] = "PATH=/usr/bin:/bin"
		}
	}
	return f
}

// skill folders and authorized_keys live under $YONDER_HOME/home
func (f *fleet) userHome() string { return filepath.Join(f.home, "yonder", "home") }

func TestAgentsInstall(t *testing.T) {
	f := newBareFleet(t)
	f.must("init", "--yes")
	home := f.userHome()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := f.must("agents", "install", "--dry-run"); !strings.Contains(r.stdout, "would install for codex") {
		t.Fatalf("dry run: %s", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote files")
	}
	r := f.must("agents", "install")
	if !strings.Contains(r.stdout, "skill installed for codex: ~/.agents/skills/yonder") || !strings.Contains(r.stdout, "skill skipped for claude-code") {
		t.Fatalf("install: %s", r.stdout)
	}
	skillDir := filepath.Join(home, ".agents", "skills", "yonder")
	if b, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md")); err != nil || !strings.Contains(string(b), "name: yonder") {
		t.Fatalf("SKILL.md: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(skillDir, ".yonder-version")); !strings.HasPrefix(string(b), testVersion+" ") {
		t.Fatalf(".yonder-version: %q", b)
	}
	// a hand edit is outdated; reinstall restores it
	if err := os.WriteFile(filepath.Join(skillDir, ".yonder-version"), []byte("0.0.1 000000000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var st struct {
		Harnesses []struct {
			Name, State, Installed string `json:",omitempty"`
			Detected               bool
		}
	}
	r = f.must("agents", "status", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &st); err != nil {
		t.Fatal(err)
	}
	if st.Harnesses[1].Name != "codex" || st.Harnesses[1].State != "outdated" || !st.Harnesses[1].Detected || st.Harnesses[0].Detected {
		t.Fatalf("status: %s", r.stdout)
	}
	if r := f.must("agents", "install", "--harness", "hermes", "--harness", "codex"); !strings.Contains(r.stdout, "skill updated for codex") || !strings.Contains(r.stdout, "skill installed for hermes: ~/.hermes/skills/yonder") {
		t.Fatalf("forced install: %s", r.stdout)
	}
	if r := f.run("", "agents", "install", "--harness", "nope"); r.code != 2 {
		t.Fatalf("unknown harness: %+v", r)
	}
	r = f.must("agents", "uninstall")
	if !strings.Contains(r.stdout, "skill removed for codex, pi") || !strings.Contains(r.stdout, "skill removed for hermes") || !strings.Contains(r.stdout, "skill not installed for claude-code") {
		t.Fatalf("uninstall: %s", r.stdout)
	}
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Fatal("uninstall left the folder")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); err != nil {
		t.Fatal("uninstall must not touch the harness's own dir")
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveLost(t *testing.T) {
	f := newBareFleet(t)
	f.must("init", "--yes")
	cfg := filepath.Join(f.home, "yonder", "config")
	writeFile(t, filepath.Join(cfg, "machines", "old-server.toml"), `name = "old-server"
os = "linux"
arch = "amd64"
tailscale_ip = "100.64.0.5"
ssh_user = "me"
ssh_port = 22
yonder_path = "/home/me/.local/bin/yonder"
ssh_host_key = "ssh-ed25519 AAAAhostkey"
ssh_client_keys = ["`+lostKey+`"]
updated_at = 2026-11-01T00:00:00Z
discovered_at = 2026-11-01T00:00:00Z
added_at = 2026-11-01T00:00:00Z
`)
	ak := filepath.Join(f.userHome(), ".ssh", "authorized_keys")
	orig := otherKey + "\n" + lostKey + "\n# a comment\nrestrict ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlostkey other-comment\n"
	writeFile(t, ak, orig)

	if r := f.run("", "remove", "laptop"); r.code != 1 || !strings.Contains(r.stderr, "laptop is this machine") {
		t.Fatalf("remove self: %+v", r)
	}
	if r := f.run("", "remove", "old-server", "--lost"); r.code != 2 || !strings.Contains(r.stderr, "--yes") {
		t.Fatalf("no confirmation: %+v", r)
	}
	r := f.must("remove", "old-server", "--lost", "--yes")
	for _, want := range []string{"removed old-server from the registry", "laptop: removed 2 line(s)", "Tailscale admin console", "Service accounts", "Machine accounts"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("remove output lacks %q:\n%s", want, r.stdout)
		}
	}
	if b, _ := os.ReadFile(ak); string(b) != otherKey+"\n# a comment\n" {
		t.Fatalf("authorized_keys: %q", b)
	}
	if baks, _ := filepath.Glob(ak + ".yonder-bak.*"); len(baks) != 1 {
		t.Fatalf("backups: %v", baks)
	} else if b, _ := os.ReadFile(baks[0]); string(b) != orig {
		t.Fatalf("backup: %q", b)
	}
	if _, err := os.Stat(filepath.Join(cfg, "machines", "old-server.toml")); !os.IsNotExist(err) {
		t.Fatal("record should be gone")
	}
	if b, err := os.ReadFile(filepath.Join(cfg, "removed", "old-server.toml")); err != nil || !strings.Contains(string(b), "lost = true") {
		t.Fatalf("tombstone: %s %v", b, err)
	}
	if r := f.must("log", "--json", "--limit", "0"); !strings.Contains(r.stdout, `"command": "yonder remove old-server --lost"`) {
		t.Fatalf("removal should be logged: %s", r.stdout)
	}

	// the lost machine comes back with a freshly refreshed record of itself: the tombstone still wins
	ghost := `{"machines":[{"name":"old-server","updated_at":"2099-01-01T00:00:00Z","added_at":"2026-11-01T00:00:00Z","facts":{}}],"tombstones":[]}`
	if r := f.run(ghost, "_sync"); r.code != 0 || strings.Contains(r.stdout, `"name":"old-server","os"`) || !strings.Contains(r.stdout, `"lost":true`) {
		t.Fatalf("_sync resurrected the lost machine: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(cfg, "machines", "old-server.toml")); !os.IsNotExist(err) {
		t.Fatal("ghost record written")
	}

	// doctor fails while a lost key is trusted again, and re-running remove fixes it
	writeFile(t, ak, otherKey+"\n"+lostKey+"\n")
	if r := f.run("", "doctor"); r.code != 1 || !strings.Contains(r.stdout, "lost machine old-server") || !strings.Contains(r.stdout, "fix: yonder remove old-server --lost --yes") {
		t.Fatalf("doctor should fail on a lost key: %+v", r)
	}
	if r := f.must("remove", "old-server", "--lost", "--yes"); !strings.Contains(r.stdout, "removed 1 line(s)") {
		t.Fatalf("rerun: %s", r.stdout)
	}
	var rep struct {
		OK     bool
		Checks []struct{ Name, Status string }
	}
	r = f.run("", "doctor", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil || r.code != 0 || !rep.OK {
		t.Fatalf("doctor --json: %v %+v", err, r)
	}

	// graceful remove of an offline machine
	f.run("", "add", "gpu-box", "--yes")
	r = f.must("remove", "gpu-box", "--yes")
	if !strings.Contains(r.stdout, "gpu-box wasn't reachable") || !strings.Contains(r.stdout, "yonder remove --forget-local") {
		t.Fatalf("graceful: %s", r.stdout)
	}
	if r := f.run("", "remove", "gpu-box", "--yes"); r.code != 1 || !strings.Contains(r.stderr, "already removed") {
		t.Fatalf("second remove: %+v", r)
	}
	if r := f.must("remove", "--forget-local", "--yes"); !strings.Contains(r.stdout, "cleared") {
		t.Fatalf("forget-local: %s", r.stdout)
	}
	if r := f.run("", "ls"); !strings.Contains(r.stdout, "yonder init") {
		t.Fatalf("after forget-local: %+v", r)
	}
}

func TestRemoveClampsFutureAddedAt(t *testing.T) {
	f := newBareFleet(t)
	f.must("init", "--yes")
	cfg := filepath.Join(f.home, "yonder", "config")
	writeFile(t, filepath.Join(cfg, "machines", "far-box.toml"), `name = "far-box"
tailscale_ip = "100.64.0.8"
ssh_host_key = "ssh-ed25519 AAAAhostkey"
updated_at = 2099-01-01T00:00:00Z
added_at = 2099-01-01T00:00:00Z
`)
	f.must("remove", "far-box", "--yes")
	if _, err := os.Stat(filepath.Join(cfg, "machines", "far-box.toml")); !os.IsNotExist(err) {
		t.Fatal("record should be gone")
	}
	b, err := os.ReadFile(filepath.Join(cfg, "removed", "far-box.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var at time.Time
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "removed_at = "); ok {
			at, _ = time.Parse(time.RFC3339, strings.TrimSpace(v))
		}
	}
	if at.IsZero() || at.After(time.Now().Add(5*time.Minute)) {
		t.Fatalf("tombstone dated past the skew limit: %s", b)
	}
}
