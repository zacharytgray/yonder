package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

func strp(s string) *string { return &s }

func TestSetSelfAndPeer(t *testing.T) {
	st := &tailnet.Status{Self: peer("laptop", true), Peers: []tailnet.Peer{peer("gpu-box", true), peer("pi", false)}}
	var ran string
	fake := &fakeSSH{yonder: func(_ transport.Target, args []string, stdio transport.IO) (int, error) {
		switch args[0] {
		case "_exec":
			all, _ := io.ReadAll(stdio.Stdin)
			var env remote.Envelope
			if err := json.Unmarshal(all, &env); err != nil {
				return 1, err
			}
			ran = env.Command
			return 0, nil
		case "_sync":
			// gpu-box answers with its own record, changed by the set it ran
			own := registry.Machine{
				Name: "gpu-box", TailscaleIP: "100.64.0.9", SSHHostKey: "ssh-ed25519 AAAAhost",
				Role: "server", Notes: "always on", UpdatedAt: time.Now().UTC(),
				Facts: registry.Facts{Hostname: "gpu-box", CPUCores: 16},
			}
			return 0, json.NewEncoder(stdio.Stdout).Encode(registry.Bundle{Machines: []registry.Machine{own}})
		}
		return 1, fmt.Errorf("unexpected %v", args)
	}}
	a, out := testApp(t, fake, st)
	if err := a.store.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour)
	for _, m := range []registry.Machine{
		{Name: "laptop", TailscaleIP: "100.64.0.1", Role: "laptop", Facts: registry.Facts{Hostname: "laptop", CPUCores: 8}},
		{Name: "gpu-box", TailscaleIP: "100.64.0.9", SSHHostKey: "ssh-ed25519 AAAAhost", Role: "gpu", Notes: "heavy jobs", Facts: registry.Facts{Hostname: "gpu-box", CPUCores: 16}},
		{Name: "pi", TailscaleIP: "100.64.0.3", SSHHostKey: "ssh-ed25519 AAAAhost", Notes: "old"},
	} {
		m.UpdatedAt, m.AddedAt, m.DiscoveredAt = old, old, old
		if err := a.store.SaveMachine(m); err != nil {
			t.Fatal(err)
		}
	}

	// this machine: notes only, role and facts kept, updated_at moves
	if err := a.setCommand("laptop", setChange{notes: strp("on battery")}, false); err != nil {
		t.Fatal(err)
	}
	m, _ := a.store.Machine("laptop")
	if m.Role != "laptop" || m.Notes != "on battery" || m.Facts.CPUCores != 8 || !m.UpdatedAt.After(old) || !m.DiscoveredAt.Equal(old) {
		t.Fatalf("self record %+v", m)
	}

	// a peer runs set on itself, and the sync brings its record back here
	if err := a.setCommand("gpu-box", setChange{role: strp("server"), notes: strp("always on")}, false); err != nil {
		t.Fatal(err)
	}
	if ran != "yonder set gpu-box --here --role server --notes 'always on'" {
		t.Fatalf("remote command %q", ran)
	}
	m, _ = a.store.Machine("gpu-box")
	if m.Role != "server" || m.Notes != "always on" || m.SSHHostKey != "ssh-ed25519 AAAAhost" || m.Facts.CPUCores != 16 {
		t.Fatalf("peer record %+v", m)
	}
	if !strings.Contains(out.String(), `set gpu-box: role server, notes "always on"`) {
		t.Fatalf("output: %s", out.String())
	}

	// an offline peer: 75, and nothing changes
	err := a.setCommand("pi", setChange{notes: strp("new")}, false)
	if ExitCode(err) != ExitTempErr || !strings.Contains(err.Error(), "nothing changed") {
		t.Fatalf("offline set: %v", err)
	}
	if m, _ = a.store.Machine("pi"); m.Notes != "old" {
		t.Fatalf("offline record changed: %+v", m)
	}
}

func TestSetArgs(t *testing.T) {
	for _, args := range [][]string{
		{"set", "laptop"},
		{"set", "laptop", "--role", "toaster"},
		{"set"},
	} {
		cmd := newRoot()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if code := ExitCode(cmd.Execute()); code != ExitUsage {
			t.Fatalf("%v: exit %d, want usage", args, code)
		}
	}
}
