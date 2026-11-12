package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/secrets"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

// fakeOp logs its argv and plays a signed-in person's op: vaults, service
// accounts (dummy tokens), and `user get --me` for a service account token.
const fakeOpScript = `#!/bin/sh
echo "$*" >> "$OP_STATE/argv"
case "$1 $2" in
"--version "*) echo 2.34.0 ;;
"account list") echo '[{"url":"example.1password.com"}]' ;;
"vault list") if [ -f "$OP_STATE/vault" ]; then printf '[{"id":"v1","name":"%s"}]\n' "$(cat "$OP_STATE/vault")"; else echo '[]'; fi ;;
"vault create") printf %s "$3" > "$OP_STATE/vault" ;;
"service-account create")
  n=$(cat "$OP_STATE/n" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$OP_STATE/n"
  echo "ops_DUMMYtoken$n" ;;
"user get") [ -n "$OP_SERVICE_ACCOUNT_TOKEN" ] && echo '{"type":"SERVICE_ACCOUNT"}' || exit 1 ;;
*) echo "[ERROR] 2026/11/11 20:00:00 unsupported" >&2; exit 1 ;;
esac
`

// fakeRemoteYonder plays a peer's yonder for the hidden secret commands.
// probe.json says what the peer has; provisioning saves the token it got.
const fakeRemoteYonder = `#!/bin/sh
dir=$(dirname "$0")
case "$1" in
_secret-probe)
  if [ ! -f "$dir/token" ]; then cat "$dir/probe.json"
  elif [ "$2" = --verify ]; then echo '{"op":"/x/op","verified":true}'
  else printf '{"op":"/x/op","provider":"1password","mode":"service-account","has_token":true,"service_account":"%s"}\n' "$(cut -d' ' -f1 "$dir/args")"
  fi ;;
_secret-provision)
  printf %s "$YONDER_OP_TOKEN" > "$dir/token"; echo "$2 $3 $4" > "$dir/args"
  echo "echoing $YONDER_OP_TOKEN" >&2
  echo "token stored in fake keychain" ;;
*) exit 9 ;;
esac
`

const oldRemoteYonder = `#!/bin/sh
echo "yonder: unknown command \"$1\" for \"yonder\"" >&2
exit 2
`

type provFleet struct {
	a     *app
	out   interface{ String() string }
	state string // fake op's dir
	peers string // one dir per peer
}

func newProvFleet(t *testing.T) *provFleet {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	bin, state, peers := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "op"), []byte(fakeOpScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("OP_STATE", state)
	old := secrets.HasDesktopApp
	secrets.HasDesktopApp = func() bool { return true }
	t.Cleanup(func() { secrets.HasDesktopApp = old })

	st := &tailnet.Status{Self: peer("laptop", true), Peers: []tailnet.Peer{
		peer("gpu-box", true), peer("no-op-box", true), peer("old-box", true),
		peer("has-tok", true), peer("off-box", false),
	}}
	fake := &fakeSSH{yonder: func(_ transport.Target, args []string, stdio transport.IO) (int, error) {
		switch args[0] {
		case "_exec": // the real target side, running the peer's fake yonder
			return remote.Exec(t.Context(), nil, stdio.Stdin, stdio.Stdout, stdio.Stderr), nil
		case "_sync":
			_, err := io.Copy(stdio.Stdout, stdio.Stdin)
			return 0, err
		}
		return 1, nil
	}}
	a, out := testApp(t, fake, st)
	// tokens for this machine go to a file under YONDER_HOME, never the real keychain
	if err := secrets.SaveSettings(secrets.Settings{TokenStore: "file"}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	probes := map[string]string{
		"gpu-box":   `{"os":"linux","op":"/usr/bin/op"}`,
		"no-op-box": `{"os":"darwin","op":""}`,
		"has-tok":   `{"os":"linux","op":"/usr/bin/op","provider":"1password","mode":"service-account","has_token":true,"service_account":"yonder-has-tok"}`,
		"off-box":   `{"os":"linux","op":"/usr/bin/op"}`,
	}
	for _, n := range []string{"laptop", "gpu-box", "no-op-box", "old-box", "has-tok", "off-box"} {
		m := registry.Machine{Name: n, OS: "linux", TailscaleIP: "100.64.0.9", SSHUser: "me", SSHPort: 22, SSHHostKey: "ssh-ed25519 AAAAhost"}
		if n != "laptop" {
			dir := filepath.Join(peers, n)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			script := fakeRemoteYonder
			if n == "old-box" {
				script = oldRemoteYonder
			}
			m.YonderPath = filepath.Join(dir, "yonder")
			if err := os.WriteFile(m.YonderPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "probe.json"), []byte(probes[n]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := a.store.SaveMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	return &provFleet{a: a, out: out, state: state, peers: peers}
}

func (f *provFleet) argv(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(f.state, "argv"))
	return string(b)
}

func (f *provFleet) noTokenLeak(t *testing.T) {
	t.Helper()
	logData, _ := os.ReadFile(registry.LogPath())
	for name, s := range map[string]string{"op argv": f.argv(t), "output": f.out.String(), "log.jsonl": string(logData)} {
		if strings.Contains(s, "ops_DUMMY") {
			t.Fatalf("a token leaked into %s:\n%s", name, s)
		}
	}
}

func TestProvisionDefaultMachines(t *testing.T) {
	f := newProvFleet(t)
	err := f.a.provision(provisionOpts{})
	out := f.out.String()
	if ExitCode(err) != ExitError {
		t.Fatalf("old-box fails, so exit 1; got %v\n%s", err, out)
	}
	for _, want := range []string{
		"off-box: skipped, offline; later: yonder secret setup --machines off-box",
		"no-op-box: skipped, the 1Password CLI `op` is not installed there. Install it (brew install 1password-cli)",
		"old-box: failed, yonder on old-box is too old for this; upgrade yonder on old-box",
		"has-tok: skipped, it already has a 1Password service account token (yonder-has-tok)",
		"--replace",
		`Agent secrets go in the 1Password vault "Yonder Agents". The agents on gpu-box can read everything in it.`,
		`created the vault "Yonder Agents"`,
		`gpu-box: ok, service account yonder-gpu-box reads "Yonder Agents", token in fake keychain`,
		"laptop: 1password (desktop)",
		"https://start.1password.com/ > Developer > Service accounts",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	argv := f.argv(t)
	if strings.Count(argv, "service-account create") != 1 ||
		!strings.Contains(argv, "service-account create yonder-gpu-box --vault Yonder Agents:read_items --raw\n") ||
		strings.Count(argv, "vault create Yonder Agents") != 1 {
		t.Fatalf("op calls:\n%s", argv)
	}
	tok, _ := os.ReadFile(filepath.Join(f.peers, "gpu-box", "token"))
	args, _ := os.ReadFile(filepath.Join(f.peers, "gpu-box", "args"))
	if string(tok) != "ops_DUMMYtoken1" || strings.TrimSpace(string(args)) != "yonder-gpu-box Yonder Agents" {
		t.Fatalf("gpu-box got %q %q", tok, args)
	}
	if m, _ := f.a.store.Machine("gpu-box"); m.SecretsAccount != "yonder-gpu-box" {
		t.Fatalf("record: %+v", m)
	}
	if s, _ := secrets.LoadSettings(); s.Provider != "1password" || s.Mode != "desktop" {
		t.Fatalf("this machine keeps desktop mode: %+v", s)
	}
	logData, _ := os.ReadFile(registry.LogPath())
	if !strings.Contains(string(logData), `"secrets":["YONDER_OP_TOKEN"]`) {
		t.Fatalf("the target logs the secret's name: %s", logData)
	}
	f.noTokenLeak(t)

	// a rerun finds the vault and leaves gpu-box alone without --replace
	f.out.(interface{ Reset() }).Reset()
	if err := f.a.provision(provisionOpts{machines: []string{"gpu-box"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(f.argv(t), "vault create") != 1 || strings.Count(f.argv(t), "service-account create") != 1 ||
		!strings.Contains(f.out.String(), "gpu-box: skipped, it already has a 1Password service account token (yonder-gpu-box)") {
		t.Fatalf("op calls:\n%s\noutput:\n%s", f.argv(t), f.out.String())
	}
}

func TestProvisionReplace(t *testing.T) {
	f := newProvFleet(t)
	old := askYesNo
	t.Cleanup(func() { askYesNo = old })
	asked := 0
	askYesNo = func(string, bool) bool { asked++; return false }

	if err := f.a.provision(provisionOpts{machines: []string{"has-tok"}, interactive: true}); err != nil {
		t.Fatal(err)
	}
	if asked != 1 || strings.Contains(f.argv(t), "service-account create") || strings.Contains(f.argv(t), "vault") {
		t.Fatalf("declined: asked %d, op calls:\n%s", asked, f.argv(t))
	}

	if err := f.a.provision(provisionOpts{machines: []string{"has-tok"}, replace: true, expiresIn: "30d"}); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	argv := f.argv(t)
	// a new name, so the old account can be told apart and revoked
	if !strings.Contains(argv, "service-account create yonder-has-tok-") || !strings.Contains(argv, ":read_items --raw --expires-in 30d") {
		t.Fatalf("op calls:\n%s", argv)
	}
	if !strings.Contains(f.out.String(), "revoke the old account yonder-has-tok at https://start.1password.com/") {
		t.Fatalf("output:\n%s", f.out.String())
	}
	f.noTokenLeak(t)
}

func TestProvisionThisMachine(t *testing.T) {
	f := newProvFleet(t)
	if err := f.a.provision(provisionOpts{machines: []string{"laptop"}, vault: "Agents"}); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	s, _ := secrets.LoadSettings()
	if s.Mode != "service-account" || s.ServiceAccount != "yonder-laptop" || s.Vault != "Agents" || s.TokenStore != "file" {
		t.Fatalf("settings: %+v", s)
	}
	tok, err := os.ReadFile(filepath.Join(registry.ConfigDir(), "tokens", "1password"))
	if err != nil || strings.TrimSpace(string(tok)) != "ops_DUMMYtoken1" {
		t.Fatalf("token file: %q %v", tok, err)
	}
	if m, _ := f.a.store.Machine("laptop"); m.SecretsAccount != "yonder-laptop" {
		t.Fatalf("self record: %+v", m)
	}
	if !strings.Contains(f.out.String(), `laptop: ok, service account yonder-laptop reads "Agents"`) {
		t.Fatalf("output:\n%s", f.out.String())
	}
	f.noTokenLeak(t)

	// usage errors before anything runs
	for _, o := range []provisionOpts{{vault: "Personal"}, {expiresIn: "soon"}, {machines: []string{"nope"}}} {
		if err := f.a.provision(o); ExitCode(err) != ExitUsage {
			t.Errorf("%+v: got %v", o, err)
		}
	}
}

func TestProvisionAllowWrite(t *testing.T) {
	f := newProvFleet(t)
	if err := f.a.provision(provisionOpts{machines: []string{"gpu-box", "laptop"}, allowWrite: true}); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	argv := f.argv(t)
	for _, m := range []string{"gpu-box", "laptop"} {
		if !strings.Contains(argv, "service-account create yonder-"+m+" --vault Yonder Agents:read_items,write_items --raw\n") {
			t.Fatalf("op calls:\n%s", argv)
		}
	}
	out := f.out.String()
	for _, want := range []string{
		"The agents on gpu-box, laptop can read, change and delete everything in it.",
		`gpu-box: ok, service account yonder-gpu-box reads and writes "Yonder Agents"`,
		"yonder secret set NAME",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	args, _ := os.ReadFile(filepath.Join(f.peers, "gpu-box", "args"))
	if strings.TrimSpace(string(args)) != "yonder-gpu-box Yonder Agents --write" {
		t.Fatalf("gpu-box got %q", args)
	}
	if m, _ := f.a.store.Machine("gpu-box"); !m.SecretsWrite {
		t.Fatalf("record: %+v", m)
	}
	if m, _ := f.a.store.Machine("laptop"); !m.SecretsWrite || m.SecretsAccount != "yonder-laptop" {
		t.Fatalf("self record: %+v", m)
	}
	if s, _ := secrets.LoadSettings(); !s.Write || s.Access() != "read and write" {
		t.Fatalf("settings: %+v", s)
	}
	f.noTokenLeak(t)
}

func TestProvisionRecordsVaultForDesktop(t *testing.T) {
	f := newProvFleet(t)
	if err := f.a.provision(provisionOpts{machines: []string{"gpu-box"}, vault: "Agents"}); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	if s, _ := secrets.LoadSettings(); s.Mode != "desktop" || s.Vault != "Agents" {
		t.Fatalf("this machine: %+v", s)
	}
	if m, _ := f.a.store.Machine("gpu-box"); m.SecretsWrite {
		t.Fatalf("read-only by default: %+v", m)
	}
}

func TestRemoveLostNamesServiceAccount(t *testing.T) {
	f := newProvFleet(t)
	m, _ := f.a.store.Machine("off-box")
	m.SecretsAccount = "yonder-off-box"
	if err := f.a.store.SaveMachine(m); err != nil {
		t.Fatal(err)
	}
	if err := f.a.removeCommand("off-box", true, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "Revoke its 1Password service account yonder-off-box") {
		t.Fatalf("output:\n%s", f.out.String())
	}
	if tomb, _ := f.a.store.Tombstone("off-box"); tomb.SecretsAccount != "yonder-off-box" {
		t.Fatalf("tombstone: %+v", tomb)
	}
}
