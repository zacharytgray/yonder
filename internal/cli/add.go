package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

type addOpts struct {
	user        string
	port        int
	role        string
	notes       string
	identity    string // ssh key for this machine, as typed (~ unexpanded); stored locally
	interactive bool
}

func newAddCmd() *cobra.Command {
	var o addOpts
	var yes bool
	cmd := &cobra.Command{
		Use:   "add <machine>",
		Short: "Add a tailnet peer to the fleet",
		Long: `Add a tailnet peer to the fleet: check ssh, pin its host key, find or install
yonder there, gather its facts, and sync the registry to every online machine.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			o.interactive = !yes && isTerminal(os.Stdin)
			if err := checkIdentity(o.identity); err != nil {
				return &usageError{err}
			}
			return a.addCommand(args[0], o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.user, "user", "", "ssh user on the machine (default: the current user)")
	f.IntVar(&o.port, "port", 0, "ssh port (default 22)")
	f.StringVar(&o.role, "role", "", "laptop | desktop | server | gpu | other")
	f.StringVar(&o.notes, "notes", "", "free text shown when the machine is offline")
	f.StringVar(&o.identity, "identity", "", "ssh private key to use for this machine (kept on this machine only)")
	f.BoolVar(&yes, "yes", false, "never prompt")
	return cmd
}

func (a *app) addCommand(name string, o addOpts) error {
	self, err := a.selfName()
	if err != nil {
		return err
	}
	if err := registry.ValidName(name); err != nil {
		return &usageError{err}
	}
	if name == self {
		return fmt.Errorf("%s is this machine; `yonder refresh %s` updates its record", name, name)
	}
	st, err := a.status()
	if err != nil {
		return err
	}
	p, ok := st.Lookup(name)
	if !ok {
		return fmt.Errorf("%s is not a peer in this tailnet; `tailscale status` lists them", name)
	}
	if p.Mobile() {
		return fmt.Errorf("%s is a %s device; yonder runs on macOS and Linux only", name, p.OS)
	}
	o = a.withDefaults(name, o)
	if !p.Online {
		if err := a.savePending(p, o); err != nil {
			return err
		}
		return withCode(ExitTempErr, "%s is offline (last seen %s); saved as pending, `yonder refresh %s` finishes setup once it is online", name, ago(p.LastSeen), name)
	}
	m, err := a.addPeer(p, o)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "added %s (%s/%s, %s@%s)\n", m.Name, m.OS, m.Arch, m.SSHUser, m.TailscaleIP)
	a.syncFleet(self)
	return nil
}

// withDefaults fills user, port, role and notes from an existing record, then the local user.
func (a *app) withDefaults(name string, o addOpts) addOpts {
	if old, err := a.store.Machine(name); err == nil {
		if o.user == "" {
			o.user = old.SSHUser
		}
		if o.port == 0 {
			o.port = old.SSHPort
		}
		if o.role == "" {
			o.role = old.Role
		}
		if o.notes == "" {
			o.notes = old.Notes
		}
	}
	if o.identity == "" {
		o.identity = a.store.Identity(name)
	}
	if o.user == "" {
		o.user = localUser()
	}
	if o.port == 0 {
		o.port = 22
	}
	return o
}

func (a *app) savePending(p tailnet.Peer, o addOpts) error {
	now := time.Now().UTC()
	m := registry.Machine{
		Name: p.Name(), OS: p.GOOS(), TailscaleIP: p.IP(), DNSName: p.DNSName,
		SSHUser: o.user, SSHPort: o.port, Role: o.role, Notes: o.notes,
		SSHClientKeys: []string{}, Pending: true, UpdatedAt: now, AddedAt: now,
	}
	if err := a.store.ClearLost(m.Name); err != nil {
		return err
	}
	if err := a.saveIdentity(m.Name, o.identity); err != nil {
		return err
	}
	_, err := a.store.MergeIn(registry.Bundle{Machines: []registry.Machine{m}})
	return err
}

// probeScript finds yonder and reports os, arch, home and client keys, one key=value per line.
// command -v echoes the PATH entry as written, so an entry like ~/.local/share/../bin
// gave an uncleaned path. the dir is resolved with pwd -P; the file is left alone so brew links stay.
const probeScript = `p=$(sh -lc 'command -v yonder' 2>/dev/null | tail -n 1)
if [ -n "$p" ]; then d=$(cd "$(dirname "$p")" 2>/dev/null && pwd -P) && p="$d/$(basename "$p")"; fi
for c in "$p" "$HOME/.local/bin/yonder" /opt/homebrew/bin/yonder /usr/local/bin/yonder /home/linuxbrew/.linuxbrew/bin/yonder "$HOME/go/bin/yonder"; do
  if [ -n "$c" ] && [ -x "$c" ]; then echo "yonder=$c"; break; fi
done
echo "os=$(uname -s)"
echo "arch=$(uname -m)"
echo "home=$HOME"
for f in "$HOME"/.ssh/*.pub; do [ -f "$f" ] && echo "pub=$(head -n 1 "$f")"; done
exit 0`

type probe struct {
	yonder, os, arch, home string
	pubs                   []string
}

func parseProbe(out string) probe {
	var p probe
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "yonder":
			if v != "" {
				p.yonder = path.Clean(v) // posix path from the remote, not filepath
			}
		case "os":
			p.os = unameOS(v)
		case "arch":
			p.arch = unameArch(v)
		case "home":
			p.home = v
		case "pub":
			if v = strings.TrimSpace(v); v != "" {
				p.pubs = append(p.pubs, v)
			}
		}
	}
	return p
}

func unameOS(s string) string {
	return strings.ToLower(strings.TrimSpace(s)) // Darwin -> darwin, Linux -> linux
}

func unameArch(s string) string {
	switch s = strings.TrimSpace(s); s {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return s
}

// addPeer does the full add for an online peer and saves the record.
func (a *app) addPeer(p tailnet.Peer, o addOpts) (registry.Machine, error) {
	name := p.Name()
	t := transport.Target{Name: name, Host: p.IP(), Port: o.port, User: o.user, Identity: registry.ExpandHome(o.identity)}

	var out, errb bytes.Buffer
	key, code, err := a.ssh.Pin(a.ctx, t, probeScript, transport.IO{Stdout: &out, Stderr: &errb})
	authFailed := func() bool {
		ue, ok := transport.IsUnreachable(err)
		return ok && ue.AuthFailed() && key != ""
	}
	if authFailed() {
		t.HostKey = key
		if o.interactive && o.identity == "" {
			if id := a.askIdentity(name); id != "" {
				o.identity, t.Identity = id, registry.ExpandHome(id)
				out.Reset()
				errb.Reset()
				code, err = a.ssh.Shell(a.ctx, t, probeScript, transport.IO{Stdout: &out, Stderr: &errb})
			}
		}
	}
	if authFailed() {
		copyLine := fmt.Sprintf("ssh-copy-id -p %d %s@%s", t.Port, o.user, t.Host)
		if o.identity != "" {
			copyLine = fmt.Sprintf("ssh-copy-id -i %s -p %d %s@%s", o.identity, t.Port, o.user, t.Host)
		}
		if !o.interactive || !a.confirm(fmt.Sprintf("ssh key auth to %s@%s failed. Copy your key there with ssh-copy-id now?", o.user, t.Host)) {
			return registry.Machine{}, fmt.Errorf("ssh key auth to %s failed; run `%s`, then `yonder add %s` again. If you reach it with a specific key, run `yonder add %s --identity <key>` instead", name, copyLine, name, name)
		}
		if err := a.ssh.CopyID(a.ctx, t, transport.IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}); err != nil {
			return registry.Machine{}, fmt.Errorf("ssh-copy-id: %w", err)
		}
		out.Reset()
		errb.Reset()
		code, err = a.ssh.Shell(a.ctx, t, probeScript, transport.IO{Stdout: &out, Stderr: &errb})
	}
	if ue, ok := transport.IsUnreachable(err); ok {
		return registry.Machine{}, withCode(ExitTempErr, "%s", ue.Error())
	}
	if err != nil {
		return registry.Machine{}, err
	}
	if code != 0 {
		return registry.Machine{}, fmt.Errorf("probing %s failed (exit %d): %s", name, code, strings.TrimSpace(errb.String()))
	}
	if key == "" {
		return registry.Machine{}, fmt.Errorf("could not read %s's ssh host key", name)
	}
	// ssh works with this key, so keep it before any call that goes through the store
	if err := a.saveIdentity(name, o.identity); err != nil {
		return registry.Machine{}, err
	}
	t.HostKey = key
	pr := parseProbe(out.String())
	if pr.os == "" || pr.home == "" {
		return registry.Machine{}, fmt.Errorf("unexpected probe output from %s: %q", name, out.String())
	}

	if pr.yonder == "" {
		path, err := a.installYonder(t, pr)
		if err != nil {
			return registry.Machine{}, err
		}
		pr.yonder = path
	}
	t.YonderPath = pr.yonder

	now := time.Now().UTC()
	m := registry.Machine{
		Name: name, OS: pr.os, Arch: pr.arch, TailscaleIP: p.IP(), DNSName: p.DNSName,
		SSHUser: o.user, SSHPort: o.port, YonderPath: pr.yonder,
		SSHHostKey: key, SSHClientKeys: pr.pubs, Role: o.role, Notes: o.notes,
		UpdatedAt: now, AddedAt: now,
	}
	if m.SSHClientKeys == nil {
		m.SSHClientKeys = []string{}
	}
	if old, err := a.store.Machine(name); err == nil {
		m.SecretsAccount, m.SecretsWrite = old.SecretsAccount, old.SecretsWrite // its token outlives a re-add
	}
	if f, err := a.remoteFacts(a.ssh, m); err != nil {
		a.warnf("could not read facts from %s: %v", name, err)
	} else {
		m.Facts, m.DiscoveredAt = f, now
	}
	// an explicit add is the trust moment: it clears a lost tombstone here,
	// and the next sync asks peers to clear theirs
	if err := a.store.ClearLost(name); err != nil {
		return m, err
	}
	a.clearLost = append(a.clearLost, name)
	if _, err := a.store.MergeIn(registry.Bundle{Machines: []registry.Machine{m}}); err != nil {
		return m, err
	}
	if err := a.yonderJSON(a.ssh, m, []string{"_init-self", name}, nil, &bytes.Buffer{}); err != nil {
		return m, fmt.Errorf("setting %s's own name: %w", name, err)
	}
	return m, nil
}

func (a *app) remoteFacts(r transport.Runner, m registry.Machine) (registry.Facts, error) {
	var out bytes.Buffer
	if err := a.yonderJSON(r, m, []string{"_facts"}, nil, &out); err != nil {
		return registry.Facts{}, err
	}
	var f registry.Facts
	if err := json.Unmarshal(out.Bytes(), &f); err != nil {
		return registry.Facts{}, fmt.Errorf("bad _facts reply: %w", err)
	}
	return f, nil
}

// installScriptURL is the official installer, used when this binary can't be copied over.
const installScriptURL = "https://raw.githubusercontent.com/zacharytgray/yonder/main/install.sh"

// installScript runs the installer on the target with curl, else wget.
const installScript = `if command -v curl >/dev/null 2>&1; then curl -fsSL ` + installScriptURL + ` | sh
elif command -v wget >/dev/null 2>&1; then wget -qO- ` + installScriptURL + ` | sh
else echo "neither curl nor wget found" >&2; exit 127; fi`

// the installer's default location; curl | sh exits 0 even when curl fails
const installedProbe = `test -x "$HOME/.local/bin/yonder"`

// installYonder copies this binary to ~/.local/bin/yonder when os/arch match,
// else runs the install script there.
func (a *app) installYonder(t transport.Target, pr probe) (string, error) {
	path := pr.home + "/.local/bin/yonder"
	if pr.os != runtime.GOOS || pr.arch != runtime.GOARCH {
		fmt.Fprintf(a.out, "installing yonder on %s (%s/%s) with the install script\n", t.Name, pr.os, pr.arch)
		var errb bytes.Buffer
		code, err := a.ssh.Shell(a.ctx, t, installScript, transport.IO{Stderr: &errb})
		switch {
		case err != nil:
		case code != 0:
			err = fmt.Errorf("exit %d: %s", code, lastLine(errb.String()))
		default:
			if code, err = a.ssh.Shell(a.ctx, t, installedProbe, transport.IO{}); err == nil && code != 0 {
				err = fmt.Errorf("no yonder at ~/.local/bin/yonder afterwards: %s", lastLine(errb.String()))
			}
		}
		if err != nil {
			return "", fmt.Errorf(`yonder is not installed on %s (%s/%s) and this binary is %s/%s, so it can't be copied over.
the install script failed there (%v).
install it there, then run `+"`yonder add %s`"+` again:
  brew trust --cask zacharytgray/yonder/yonder && brew install --cask zacharytgray/yonder/yonder
  or: go install github.com/zacharytgray/yonder/cmd/yonder@latest`,
				t.Name, pr.os, pr.arch, runtime.GOOS, runtime.GOARCH, err, t.Name)
		}
		fmt.Fprintf(a.out, "installed yonder on %s at %s\n", t.Name, path)
		return path, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if err := a.remoteSh(t, `mkdir -p "$HOME/.local/bin"`); err != nil {
		return "", err
	}
	if err := a.ssh.Copy(a.ctx, t, exe, ".local/bin/yonder.tmp"); err != nil {
		return "", err
	}
	if err := a.remoteSh(t, `chmod 755 "$HOME/.local/bin/yonder.tmp" && mv -f "$HOME/.local/bin/yonder.tmp" "$HOME/.local/bin/yonder"`); err != nil {
		return "", err
	}
	fmt.Fprintf(a.out, "installed yonder on %s at %s\n", t.Name, path)
	return path, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return "no output"
}

func (a *app) remoteSh(t transport.Target, script string) error {
	var errb bytes.Buffer
	code, err := a.ssh.Shell(a.ctx, t, script, transport.IO{Stderr: &errb})
	if err != nil {
		return fmt.Errorf("%s on %s: %w", script, t.Name, err)
	}
	if code != 0 {
		return fmt.Errorf("%s on %s exited %d: %s", script, t.Name, code, strings.TrimSpace(errb.String()))
	}
	return nil
}

// checkIdentity refuses a key path that isn't a file. "" is fine.
func checkIdentity(id string) error {
	if id == "" {
		return nil
	}
	st, err := os.Stat(registry.ExpandHome(id))
	switch {
	case err != nil:
		return fmt.Errorf("identity %s: %w", id, err)
	case st.IsDir():
		return fmt.Errorf("identity %s is a directory, not a private key", id)
	}
	return nil
}

// saveIdentity keeps target's key in this machine's config.toml. "" leaves it alone.
func (a *app) saveIdentity(name, id string) error {
	if id == "" || a.store.Identity(name) == id {
		return nil
	}
	return a.store.SetIdentity(name, id)
}

// askIdentity is the prompt after key auth fails. Enter skips.
func (a *app) askIdentity(name string) string {
	var id string
	err := huh.NewInput().
		Title(fmt.Sprintf("Which key do you use for %s? (Enter to skip)", name)).
		Description("path to the private key, like ~/.ssh/id_ed25519").
		Value(&id).
		Validate(checkIdentity).
		Run()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(id)
}

func (a *app) confirm(question string) bool {
	ok := true
	if err := huh.NewConfirm().Title(question).Value(&ok).Run(); err != nil {
		return false
	}
	return ok
}

// localHostKey reads this machine's sshd public host key, so peers can pin it.
func localHostKey() string {
	for _, f := range []string{"ssh_host_ed25519_key.pub", "ssh_host_ecdsa_key.pub", "ssh_host_rsa_key.pub"} {
		data, err := os.ReadFile(filepath.Join("/etc/ssh", f))
		if err != nil {
			continue
		}
		if fs := strings.Fields(string(data)); len(fs) >= 2 {
			return fs[0] + " " + fs[1]
		}
	}
	return ""
}

// localClientKeys reads ~/.ssh/*.pub.
func localClientKeys() []string {
	out := []string{}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	files, _ := filepath.Glob(filepath.Join(home, ".ssh", "*.pub"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if line, _, _ := strings.Cut(string(data), "\n"); strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}
