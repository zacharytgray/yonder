package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

// meshSSH runs shell scripts for real, each peer with its own temp home
type meshSSH struct {
	fakeSSH
	homes map[string]string
}

func (m *meshSSH) Shell(ctx context.Context, t transport.Target, script string, stdio transport.IO) (int, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+m.homes[t.Name])
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdio.Stdin, stdio.Stdout, stdio.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

const (
	laptopKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlaptop me@laptop"
	deskKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIdesk me@desk"
	gpuKey    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIgpu me@gpu-box"
	pcKey     = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIpc me@pc"
)

func keyOnly(k string) string { f := strings.Fields(k); return f[0] + " " + f[1] }

func meshFleet(t *testing.T) (*app, *meshSSH, map[string]string) {
	t.Helper()
	pc := peer("pc", false)
	pc.TailscaleIPs = []string{"100.64.0.4"}
	ps := []tailnet.Peer{peer("desk", true), peer("gpu-box", true), pc}
	ps[0].TailscaleIPs, ps[1].TailscaleIPs = []string{"100.64.0.2"}, []string{"100.64.0.3"}
	st := &tailnet.Status{Self: peer("laptop", true), Peers: ps}
	st.Self.TailscaleIPs = []string{"100.64.0.1"}
	ssh := &meshSSH{homes: map[string]string{"desk": t.TempDir(), "gpu-box": t.TempDir()}}
	ssh.yonder = func(_ transport.Target, args []string, stdio transport.IO) (int, error) {
		if args[0] == "_sync" {
			fmt.Fprint(stdio.Stdout, `{"machines":[],"tombstones":[]}`)
			return 0, nil
		}
		return 1, fmt.Errorf("unexpected %v", args)
	}
	a, _ := testApp(t, ssh, st)
	if err := a.store.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	for _, m := range []registry.Machine{
		{Name: "laptop", TailscaleIP: "100.64.0.1", SSHClientKeys: []string{laptopKey}},
		{Name: "desk", TailscaleIP: "100.64.0.2", SSHClientKeys: []string{deskKey}},
		{Name: "gpu-box", TailscaleIP: "100.64.0.3", SSHClientKeys: []string{gpuKey}},
		{Name: "pc", TailscaleIP: "100.64.0.4", SSHClientKeys: []string{pcKey}},
	} {
		m.DNSName, m.SSHHostKey, m.UpdatedAt, m.AddedAt = m.Name+".example.ts.net", "ssh-ed25519 AAAAhost", now, now
		if err := a.store.SaveMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"laptop":  transport.AuthorizedKeysPath(registry.UserHome()),
		"desk":    filepath.Join(ssh.homes["desk"], ".ssh", "authorized_keys"),
		"gpu-box": filepath.Join(ssh.homes["gpu-box"], ".ssh", "authorized_keys"),
	}
	return a, ssh, files
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestMeshAndRemove(t *testing.T) {
	a, _, files := meshFleet(t)
	out := a.out.(interface{ String() string })
	// desk already trusts the laptop by hand, gpu-box's key too, with options
	_ = os.MkdirAll(filepath.Dir(files["desk"]), 0o700)
	hand := laptopKey + "\nrestrict " + gpuKey + "\n"
	if err := os.WriteFile(files["desk"], []byte(hand), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := a.mesh("laptop", "", true, false); err != nil {
		t.Fatalf("mesh: %v\n%s", err, out.String())
	}
	want := map[string][]string{
		"laptop":  {keyOnly(deskKey) + " yonder:desk", keyOnly(gpuKey) + " yonder:gpu-box", keyOnly(pcKey) + " yonder:pc"},
		"desk":    {keyOnly(pcKey) + " yonder:pc"},
		"gpu-box": {keyOnly(laptopKey) + " yonder:laptop", keyOnly(deskKey) + " yonder:desk", keyOnly(pcKey) + " yonder:pc"},
	}
	for m, lines := range want {
		got := read(t, files[m])
		for _, l := range lines {
			if !strings.Contains(got, l+"\n") {
				t.Errorf("%s lacks %q:\n%s", m, l, got)
			}
		}
	}
	if got := read(t, files["desk"]); !strings.HasPrefix(got, hand) {
		t.Fatalf("desk's own lines changed:\n%s", got)
	}
	if !strings.Contains(out.String(), "offline, so not changed: pc") {
		t.Fatalf("pc is offline and should be named:\n%s", out.String())
	}

	// a second run changes nothing
	before := map[string]string{}
	for m, p := range files {
		before[m] = read(t, p)
	}
	if err := a.mesh("laptop", "", true, false); err != nil {
		t.Fatal(err)
	}
	for m, p := range files {
		if read(t, p) != before[m] {
			t.Fatalf("second mesh changed %s", m)
		}
	}
	if !strings.Contains(out.String(), "already trusts") {
		t.Fatalf("second run should say there's nothing to do:\n%s", out.String())
	}

	// plain remove drops the lines mesh added for gpu-box everywhere, and nothing else
	if err := a.removeCommand("gpu-box", false, true, false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, files["laptop"]); strings.Contains(got, "yonder:gpu-box") || !strings.Contains(got, "yonder:desk") {
		t.Fatalf("laptop after remove:\n%s", got)
	}
	if got := read(t, files["desk"]); !strings.Contains(got, "restrict "+gpuKey) {
		t.Fatalf("remove took out a line yonder didn't add:\n%s", got)
	}
	if !strings.Contains(out.String(), "laptop: removed 1 line(s) yonder added for gpu-box") {
		t.Fatalf("remove output:\n%s", out.String())
	}
	// gpu-box is gone, so mesh never hands its key out again
	if err := a.mesh("laptop", "", true, false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, files["laptop"]); strings.Contains(got, "yonder:gpu-box") {
		t.Fatalf("mesh re-added a removed machine:\n%s", got)
	}
}

func TestRemoveLostStripsMeshLines(t *testing.T) {
	a, ssh, files := meshFleet(t)
	if err := a.mesh("laptop", "", true, false); err != nil {
		t.Fatal(err)
	}
	// the strip-keys call to desk goes through yonder; fake it as an old peer
	ssh.yonder = func(_ transport.Target, args []string, stdio transport.IO) (int, error) {
		if args[0] == "_sync" {
			fmt.Fprint(stdio.Stdout, `{"machines":[],"tombstones":[]}`)
			return 0, nil
		}
		return 2, nil
	}
	if err := a.removeCommand("desk", true, true, false); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"laptop", "gpu-box"} {
		if got := read(t, files[m]); strings.Contains(got, "yonder:desk") {
			t.Fatalf("%s still has desk's mesh line:\n%s", m, got)
		}
	}
}

func TestMeshFocusAndSymlink(t *testing.T) {
	a, _, files := meshFleet(t)
	out := a.out.(interface{ String() string })
	real := filepath.Join(t.TempDir(), "keys")
	_ = os.MkdirAll(filepath.Dir(files["gpu-box"]), 0o700)
	_ = os.WriteFile(real, nil, 0o600)
	if err := os.Symlink(real, files["gpu-box"]); err != nil {
		t.Fatal(err)
	}
	err := a.mesh("laptop", "desk", true, false)
	if err == nil || !strings.Contains(err.Error(), "gpu-box") {
		t.Fatalf("want gpu-box to fail: %v", err)
	}
	if !strings.Contains(out.String(), "Add these 1 line(s)") || read(t, real) != "" {
		t.Fatalf("symlink should be refused with the line to add:\n%s", out.String())
	}
	// focus on desk: laptop learns desk's key only; desk learns everyone's
	if got := read(t, files["laptop"]); strings.Contains(got, "yonder:pc") || !strings.Contains(got, "yonder:desk") {
		t.Fatalf("laptop:\n%s", got)
	}
	if got := read(t, files["desk"]); !strings.Contains(got, "yonder:laptop") || !strings.Contains(got, "yonder:pc") {
		t.Fatalf("desk:\n%s", got)
	}
}
