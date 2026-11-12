package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/store"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

func init() {
	store.Register(store.Kind{Name: "testnotes", Layer: registry.LayerAgents})
	backgroundOff = true // a background sync here would re-run the test binary
}

// twoHomes sets up laptop (this process's YONDER_HOME) and gpu-box (another
// home), each knowing both. the fake ssh runs gpu-box's hidden commands in
// process under its own YONDER_HOME.
func twoHomes(t *testing.T) (*app, string, *bytes.Buffer) {
	t.Helper()
	peerHome := t.TempDir()
	st := &tailnet.Status{Self: peer("laptop", true), Peers: []tailnet.Peer{peer("gpu-box", true)}}
	var a *app
	fake := &fakeSSH{yonder: func(tg transport.Target, args []string, stdio transport.IO) (int, error) {
		mine := os.Getenv("YONDER_HOME")
		os.Setenv("YONDER_HOME", peerHome)
		defer os.Setenv("YONDER_HOME", mine)
		return remote.Dispatch(a.ctx, args, stdio.Stdin, stdio.Stdout, stdio.Stderr), nil
	}}
	a, out := testApp(t, fake, st)
	now := time.Now().UTC()
	recs := []registry.Machine{
		{Name: "laptop", TailscaleIP: "100.64.0.9", SSHHostKey: "ssh-ed25519 AAAAlaptop", UpdatedAt: now, AddedAt: now, DiscoveredAt: now},
		{Name: "gpu-box", TailscaleIP: "100.64.0.9", SSHHostKey: "ssh-ed25519 AAAAgpu", UpdatedAt: now, AddedAt: now, DiscoveredAt: now},
	}
	for _, home := range []string{os.Getenv("YONDER_HOME"), peerHome} {
		s := &registry.Store{Dir: filepath.Join(home, "config")}
		self := "laptop"
		if home == peerHome {
			self = "gpu-box"
		}
		if err := s.SetSelf(self); err != nil {
			t.Fatal(err)
		}
		for _, m := range recs {
			if err := s.SaveMachine(m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := a.store.SetLayer(registry.LayerAgents, true, "laptop"); err != nil {
		t.Fatal(err)
	}
	return a, peerHome, out
}

func writeAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAt(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestSyncCommand(t *testing.T) {
	a, peerHome, out := twoHomes(t)
	here := filepath.Join(os.Getenv("YONDER_HOME"), "data", "testnotes")
	there := filepath.Join(peerHome, "data", "testnotes")
	writeAt(t, filepath.Join(here, "a.md"), "from laptop\n")

	// the layer reaches gpu-box with the registry, then the store follows
	if err := a.syncCommand("", nil, syncOpts{}); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if readAt(filepath.Join(there, "a.md")) != "from laptop\n" {
		t.Fatalf("gpu-box should have a.md:\n%s", out)
	}
	if !strings.Contains(out.String(), "testnotes: gpu-box: get 0, send 1") {
		t.Fatalf("output: %s", out)
	}

	writeAt(t, filepath.Join(there, "b.md"), "from gpu-box\n")
	out.Reset()
	if err := a.syncCommand("testnotes", []string{"gpu-box"}, syncOpts{}); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if readAt(filepath.Join(here, "b.md")) != "from gpu-box\n" || !strings.Contains(out.String(), "got b.md (from gpu-box") {
		t.Fatalf("laptop should have b.md:\n%s", out)
	}

	// dry run moves nothing
	writeAt(t, filepath.Join(here, "c.md"), "dry\n")
	out.Reset()
	if err := a.syncCommand("", nil, syncOpts{dryRun: true}); err != nil {
		t.Fatal(err)
	}
	if readAt(filepath.Join(there, "c.md")) != "<missing>" || !strings.Contains(out.String(), "would send 1") {
		t.Fatalf("dry run: %s", out)
	}

	// turned off there: skipped, not failed
	ps := &registry.Store{Dir: filepath.Join(peerHome, "config")}
	if err := ps.SetLayer(registry.LayerAgents, false, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.syncStore("testnotes", "laptop", nil, syncOpts{}); !err {
		t.Fatalf("a peer with the layer off is skipped, not failed:\n%s", out)
	}
	if !strings.Contains(out.String(), "skipped: the agents layer is off there") {
		t.Fatalf("output: %s", out)
	}
}
