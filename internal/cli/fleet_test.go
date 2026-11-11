package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

// fakeSSH stands in for the ssh transport and records what it was asked to run.
type fakeSSH struct {
	mu      sync.Mutex
	yonders []string // "<machine> <args>"
	scripts []string
	yonder  func(t transport.Target, args []string, stdio transport.IO) (int, error)
	shell   func(script string, stdio transport.IO) (int, error)
}

func (f *fakeSSH) Yonder(_ context.Context, t transport.Target, args []string, stdio transport.IO) (int, error) {
	f.mu.Lock()
	f.yonders = append(f.yonders, t.Name+" "+strings.Join(args, " "))
	f.mu.Unlock()
	return f.yonder(t, args, stdio)
}

func (f *fakeSSH) Shell(_ context.Context, _ transport.Target, script string, stdio transport.IO) (int, error) {
	f.mu.Lock()
	f.scripts = append(f.scripts, script)
	f.mu.Unlock()
	return f.shell(script, stdio)
}

func (f *fakeSSH) Pin(context.Context, transport.Target, string, transport.IO) (string, int, error) {
	return "", 1, fmt.Errorf("not faked")
}
func (f *fakeSSH) CopyID(context.Context, transport.Target, transport.IO) error {
	return fmt.Errorf("not faked")
}
func (f *fakeSSH) Copy(context.Context, transport.Target, string, string) error {
	return fmt.Errorf("not faked")
}

func testApp(t *testing.T, ssh sshRunner, st *tailnet.Status) (*app, *bytes.Buffer) {
	t.Helper()
	t.Setenv("YONDER_HOME", t.TempDir())
	var out bytes.Buffer
	return &app{
		ctx: context.Background(), store: registry.Open(), ssh: ssh,
		out: &out, errw: &out, st: st, stDone: true,
	}, &out
}

func peer(name string, online bool) tailnet.Peer {
	return tailnet.Peer{DNSName: name + ".example.ts.net", TailscaleIPs: []string{"100.64.0.9"}, Online: online}
}

func TestLazyRefresh(t *testing.T) {
	st := &tailnet.Status{
		Self: peer("laptop", true),
		Peers: []tailnet.Peer{
			peer("fresh-box", true), peer("stale-box", true), peer("slow-box", true),
			peer("off-box", false), peer("pend-box", true),
		},
	}
	fake := &fakeSSH{yonder: func(t transport.Target, args []string, stdio transport.IO) (int, error) {
		if args[0] != "_facts" {
			return 1, fmt.Errorf("unexpected %v", args)
		}
		if t.Name == "slow-box" {
			time.Sleep(2 * time.Second) // ignores ctx on purpose: the cap must not wait for it
			return 1, fmt.Errorf("too slow")
		}
		fmt.Fprintf(stdio.Stdout, `{"hostname":%q,"cpu_cores":8}`, t.Name)
		return 0, nil
	}}
	a, _ := testApp(t, fake, st)
	old := lazyCap
	lazyCap = 200 * time.Millisecond
	defer func() { lazyCap = old }()

	now := time.Now().UTC()
	twoDays := now.Add(-48 * time.Hour)
	ms := []registry.Machine{
		{Name: "laptop", DiscoveredAt: now},
		{Name: "fresh-box", DiscoveredAt: now.Add(-time.Hour)},
		{Name: "stale-box", DiscoveredAt: twoDays},
		{Name: "slow-box", DiscoveredAt: twoDays},
		{Name: "off-box", DiscoveredAt: twoDays},
		{Name: "pend-box", Pending: true},
	}
	for i := range ms {
		ms[i].SSHHostKey, ms[i].UpdatedAt, ms[i].TailscaleIP = "ssh-ed25519 AAAAhost", ms[i].DiscoveredAt, "100.64.0.9"
		if err := a.store.SaveMachine(ms[i]); err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	if !a.lazyRefresh("laptop", ms) {
		t.Fatal("stale-box should have been refreshed")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("lazy refresh took %v, cap is %v", d, lazyCap)
	}
	fake.mu.Lock()
	calls := slices.Clone(fake.yonders)
	fake.mu.Unlock()
	slices.Sort(calls)
	if want := []string{"slow-box _facts", "stale-box _facts"}; !slices.Equal(calls, want) {
		t.Fatalf("calls %v, want %v", calls, want)
	}
	got, _ := a.store.Machine("stale-box")
	if got.Facts.CPUCores != 8 || time.Since(got.DiscoveredAt) > time.Minute {
		t.Fatalf("stale-box not updated: %+v", got)
	}
	if got, _ := a.store.Machine("slow-box"); !got.DiscoveredAt.Equal(twoDays) || got.Facts.CPUCores != 0 {
		t.Fatalf("slow-box should keep its stored facts: %+v", got)
	}
}

func TestInstallYonderScript(t *testing.T) {
	arch := "sparc64"
	if runtime.GOARCH == arch {
		arch = "s390x"
	}
	pr := probe{os: "linux", arch: arch, home: "/home/me"}
	tgt := transport.Target{Name: "gpu-box"}

	cases := []struct {
		name          string
		install, test int
		ok            bool
		scripts       int
	}{
		{"installed", 0, 0, true, 2},
		{"no release", 0, 1, false, 2},
		{"script failed", 1, 0, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeSSH{shell: func(script string, stdio transport.IO) (int, error) {
				if script == installedProbe {
					return c.test, nil
				}
				fmt.Fprintln(stdio.Stderr, "curl: (22) The requested URL returned error: 404")
				return c.install, nil
			}}
			a, _ := testApp(t, fake, nil)
			path, err := a.installYonder(tgt, pr)
			if len(fake.scripts) != c.scripts {
				t.Fatalf("ran %d scripts: %q", len(fake.scripts), fake.scripts)
			}
			s := fake.scripts[0]
			if !strings.Contains(s, "curl -fsSL "+installScriptURL+" | sh") || !strings.Contains(s, "wget -qO- "+installScriptURL+" | sh") {
				t.Fatalf("install command: %s", s)
			}
			if c.ok {
				if err != nil || path != "/home/me/.local/bin/yonder" {
					t.Fatalf("got %q, %v", path, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "brew install --cask zacharytgray/tap/yonder") || !strings.Contains(err.Error(), "go install github.com/zacharytgray/yonder") || !strings.Contains(err.Error(), "404") {
				t.Fatalf("want the manual install error, got %v", err)
			}
		})
	}
}

func TestSyncFleetStripsLostKeysHere(t *testing.T) {
	a, out := testApp(t, &fakeSSH{}, &tailnet.Status{Self: peer("laptop", true)})
	const lost = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlostkey me@old-server"
	ak := transport.AuthorizedKeysPath(registry.UserHome())
	if err := os.MkdirAll(filepath.Dir(ak), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ak, []byte("# mine\n"+lost+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SaveTombstone(registry.Tombstone{Name: "old-server", RemovedAt: time.Now().UTC(), Lost: true, SSHClientKeys: []string{lost}, HeldKeys: []string{lost}}); err != nil {
		t.Fatal(err)
	}
	a.syncFleet("laptop") // no peers online: still cleans this machine
	if b, _ := os.ReadFile(ak); string(b) != "# mine\n" {
		t.Fatalf("authorized_keys: %q", b)
	}
	if !strings.Contains(out.String(), "lost machine old-server") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestReachRefusesChangedAddress(t *testing.T) {
	fake := &fakeSSH{yonder: func(transport.Target, []string, transport.IO) (int, error) { return 0, nil }}
	a, _ := testApp(t, fake, &tailnet.Status{Self: peer("laptop", true), Peers: []tailnet.Peer{peer("gpu-box", true)}})
	m := registry.Machine{Name: "gpu-box", TailscaleIP: "100.64.0.2", SSHHostKey: "ssh-ed25519 AAAAhost"}
	_, err := a.reach(m, "laptop")
	want := "gpu-box's address changed (record 100.64.0.2, tailnet 100.64.0.9); run `yonder doctor`"
	if err == nil || err.Error() != want || ExitCode(err) != ExitError {
		t.Fatalf("got %v (exit %d)", err, ExitCode(err))
	}
	// refresh doesn't quietly move the record to the new address either
	if err := a.refreshOne(m, "laptop"); err == nil || !strings.Contains(err.Error(), "address changed") {
		t.Fatalf("refresh: %v", err)
	}
	if len(fake.yonders) != 0 {
		t.Fatalf("connected anyway: %v", fake.yonders)
	}
	m.TailscaleIP = "100.64.0.9"
	if _, err := a.reach(m, "laptop"); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshNeedsAddForMissingHostKey(t *testing.T) {
	fake := &fakeSSH{}
	a, _ := testApp(t, fake, &tailnet.Status{Self: peer("laptop", true), Peers: []tailnet.Peer{peer("gpu-box", true)}})
	m := registry.Machine{Name: "gpu-box", TailscaleIP: "100.64.0.9"}
	err := a.refreshOne(m, "laptop")
	if err == nil || !strings.Contains(err.Error(), "run `yonder add gpu-box`") {
		t.Fatalf("got %v", err)
	}
	if len(fake.yonders)+len(fake.scripts) != 0 {
		t.Fatalf("should not connect: %v %v", fake.yonders, fake.scripts)
	}
}

func TestAddClearsLostAndAsksPeers(t *testing.T) {
	var sent []string
	fake := &fakeSSH{yonder: func(t transport.Target, args []string, stdio transport.IO) (int, error) {
		b, _ := io.ReadAll(stdio.Stdin)
		sent = append(sent, string(b))
		fmt.Fprint(stdio.Stdout, `{"machines":[],"tombstones":[]}`)
		return 0, nil
	}}
	gpu := peer("gpu-box", false)
	a, _ := testApp(t, fake, &tailnet.Status{Self: peer("laptop", true), Peers: []tailnet.Peer{gpu, peer("desk", true)}})
	if err := a.store.SaveTombstone(registry.Tombstone{Name: "gpu-box", RemovedAt: time.Now().UTC(), Lost: true}); err != nil {
		t.Fatal(err)
	}
	// a local add (here the offline, pending one) clears the lost tombstone here
	if err := a.savePending(gpu, addOpts{user: "me", port: 22}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.Machine("gpu-box"); err != nil {
		t.Fatal("re-added record should be saved")
	}
	if _, ok := a.store.Tombstone("gpu-box"); ok {
		t.Fatal("lost tombstone should be cleared")
	}
	// and the sync after a full add asks peers to clear theirs
	if err := a.store.SaveMachine(registry.Machine{Name: "desk", TailscaleIP: "100.64.0.9", SSHHostKey: "ssh-ed25519 AAAAhost", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	a.clearLost = []string{"gpu-box"}
	a.syncFleet("laptop")
	if len(sent) == 0 || !strings.Contains(sent[0], `"clear_lost":["gpu-box"]`) {
		t.Fatalf("sync payload: %q", sent)
	}
}

func TestCallerLogScrubsCommand(t *testing.T) {
	a, _ := testApp(t, &fakeSSH{}, nil)
	env := remote.Envelope{ID: "id-1", Command: "curl -H 'auth: sk-example-123' x", Secrets: map[string]string{"API_KEY": "sk-example-123"}}
	a.logCaller(env, time.Now(), ExitTempErr, nil)
	raw, _ := os.ReadFile(registry.LogPath())
	if strings.Contains(string(raw), "sk-example-123") || !strings.Contains(string(raw), "auth: [API_KEY]") {
		t.Fatalf("log: %s", raw)
	}
}
