package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/discover"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/secrets"
	"github.com/zacharytgray/yonder/internal/tailnet"
)

var roles = []string{"laptop", "desktop", "server", "gpu", "other"}

func newInitCmd() *cobra.Command {
	var (
		yes      bool
		machines []string
		user     string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up this machine and pick tailnet peers to add",
		Long: `Set up this machine as a fleet member, then choose which tailnet peers to add.

Offline peers are saved as pending and finish setup on the next ` + "`yonder refresh`" + `.
Non-interactive: yonder init --yes --machines gpu-box,laptop [--user me]
With --yes and no --machines, only this machine is set up.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			interactive := !yes && isTerminal(os.Stdin)
			return a.initCommand(interactive, machines, user)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "never prompt")
	cmd.Flags().StringSliceVar(&machines, "machines", nil, "peers to add, comma-separated")
	cmd.Flags().StringVar(&user, "user", "", "ssh user on the peers (default: the current user)")
	return cmd
}

func (a *app) initCommand(interactive bool, chosen []string, user string) error {
	st, err := a.status()
	if err != nil {
		if errors.Is(err, tailnet.ErrNotInstalled) || errors.Is(err, tailnet.ErrNotRunning) {
			return err
		}
		return fmt.Errorf("reading tailscale status: %w", err)
	}
	self := st.Self.Name()
	if err := registry.ValidName(self); err != nil {
		return fmt.Errorf("this machine's tailscale name: %w", err)
	}
	rec := a.selfRecord(st.Self)
	if err := a.store.SetSelf(self); err != nil {
		return err
	}
	if _, err := a.store.MergeIn(registry.Bundle{Machines: []registry.Machine{rec}}); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "this machine is %s (%s/%s, %s)\n", self, rec.OS, rec.Arch, rec.TailscaleIP)
	if rec.SSHHostKey == "" {
		a.warnf("no sshd host key in /etc/ssh; other machines can't reach this one until sshd (Remote Login on macOS) is on")
	}

	candidates := a.initCandidates(st, self)
	picks, err := a.pickPeers(candidates, chosen, interactive)
	if err != nil {
		return err
	}

	var failed, added []string
	for _, p := range picks {
		o := addOpts{user: user, interactive: interactive}
		if interactive {
			o.role, o.notes = askRoleNotes(p.Name())
		}
		o = a.withDefaults(p.Name(), o)
		if !p.Online {
			if err := a.savePending(p, o); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s is offline (last seen %s); saved as pending, it finishes setup on the next `yonder refresh`\n", p.Name(), ago(p.LastSeen))
			continue
		}
		m, err := a.addPeer(p, o)
		if err != nil {
			a.warnf("%s: %v", p.Name(), err)
			failed = append(failed, p.Name())
			continue
		}
		fmt.Fprintf(a.out, "added %s (%s/%s, %s@%s)\n", m.Name, m.OS, m.Arch, m.SSHUser, m.TailscaleIP)
		added = append(added, m.Name)
	}

	// secrets: offer hands-free 1password for the fleet when this machine can
	if err := a.secretsStep(interactive); err != nil {
		a.warnf("secrets setup: %v", err)
	}
	// hook: step 4 installs the agent skill into each harness here
	if err := agents.InstallAll(); err != nil {
		a.warnf("agent skill install: %v", err)
	}

	a.syncFleet(self)
	a.installSkillOn(self, added, interactive)
	if len(failed) > 0 {
		return fmt.Errorf("could not add %s; fix the errors above and run `yonder add <machine>`", strings.Join(failed, ", "))
	}
	return nil
}

// secretsStep is init's secrets setup. it leaves an existing provider alone.
func (a *app) secretsStep(interactive bool) error {
	if cur, err := secrets.LoadSettings(); err == nil && cur.Provider != "" {
		return secrets.SetupInteractive(false)
	}
	if interactive {
		return a.secretsChoice(provisionOpts{}, false)
	}
	if err := secrets.SetupInteractive(false); err != nil {
		return err
	}
	// no op call here: --yes runs unattended
	if secrets.OpAndApp() {
		fmt.Fprintln(a.out, "  or hands-free 1Password for every machine, from here (one approval): yonder secret setup --yes")
	}
	return nil
}

// installSkillOn installs the skill on machines init just added, the same way
// `agents install --all-machines` does. failures are warnings.
func (a *app) installSkillOn(self string, names []string, interactive bool) {
	if len(names) == 0 {
		return
	}
	if interactive && !a.confirm("Install the skill on "+strings.Join(names, ", ")+" too?") {
		return
	}
	for _, n := range names {
		m, err := a.store.Machine(n)
		if err != nil {
			a.warnf("skill on %s: %v", n, err)
			continue
		}
		out, code, err := a.runAgents(m, self, "install", nil)
		switch {
		case err != nil:
			a.warnf("skill on %s: %v", n, err)
		case code != 0:
			a.warnf("skill on %s: exit %d (an older yonder there?): %s", n, code, lastLine(out.String()))
		default:
			fmt.Fprintf(a.out, "installed the skill on %s\n", n)
		}
	}
}

// selfRecord builds this machine's record, keeping role and notes from an old one.
func (a *app) selfRecord(p tailnet.Peer) registry.Machine {
	now := time.Now().UTC()
	exe := selfPath()
	m := registry.Machine{
		Name: p.Name(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		TailscaleIP: p.IP(), DNSName: p.DNSName,
		SSHUser: localUser(), SSHPort: 22, YonderPath: exe,
		SSHHostKey: localHostKey(), SSHClientKeys: localClientKeys(),
		UpdatedAt: now, DiscoveredAt: now, AddedAt: now, Facts: discover.Local(),
	}
	if old, err := a.store.Machine(m.Name); err == nil {
		m.Role, m.Notes, m.AddedAt, m.SecretsAccount = old.Role, old.Notes, old.AddedAt, old.SecretsAccount
		if old.SSHPort != 0 {
			m.SSHPort = old.SSHPort
		}
	}
	return m
}

type candidate struct {
	peer tailnet.Peer
	note string // why it can't be picked, if anything
}

func (a *app) initCandidates(st *tailnet.Status, self string) []candidate {
	var out []candidate
	for _, p := range st.Peers {
		name := p.Name()
		c := candidate{peer: p}
		switch {
		case name == self:
			continue
		case p.Mobile():
			c.note = p.OS + " device, skipped"
		case registry.ValidName(name) != nil:
			c.note = "name is not kebab-case, skipped"
		default:
			if old, err := a.store.Machine(name); err == nil && !old.Pending {
				c.note = "already in the fleet"
			} else if _, gone := a.store.Tombstone(name); gone {
				c.note = "removed earlier; `yonder add " + name + "` re-adds it"
			}
		}
		out = append(out, c)
	}
	return out
}

func (a *app) pickPeers(cands []candidate, chosen []string, interactive bool) ([]tailnet.Peer, error) {
	byName := map[string]candidate{}
	var open []candidate
	for _, c := range cands {
		byName[c.peer.Name()] = c
		if c.note == "" {
			open = append(open, c)
		} else if len(chosen) == 0 {
			fmt.Fprintf(a.out, "  %s: %s\n", c.peer.Name(), c.note)
		}
	}
	if len(chosen) > 0 {
		var picks []tailnet.Peer
		for _, n := range chosen {
			c, ok := byName[strings.TrimSpace(n)]
			if !ok {
				return nil, fmt.Errorf("%s is not a peer in this tailnet", n)
			}
			if c.peer.Mobile() {
				return nil, fmt.Errorf("%s is a %s device; yonder runs on macOS and Linux only", n, c.peer.OS)
			}
			picks = append(picks, c.peer)
		}
		return picks, nil
	}
	if len(open) == 0 {
		fmt.Fprintln(a.out, "no other peers to add")
		return nil, nil
	}
	if !interactive {
		names := make([]string, len(open))
		for i, c := range open {
			names[i] = c.peer.Name()
		}
		fmt.Fprintf(a.out, "peers you can add: %s\npass --machines a,b to add them, or run `yonder add <machine>` later\n", strings.Join(names, ", "))
		return nil, nil
	}
	opts := make([]huh.Option[string], len(open))
	for i, c := range open {
		label := fmt.Sprintf("%s  (%s, online)", c.peer.Name(), c.peer.GOOS())
		if !c.peer.Online {
			label = fmt.Sprintf("%s  (%s, offline %s: added as pending)", c.peer.Name(), c.peer.GOOS(), ago(c.peer.LastSeen))
		}
		opts[i] = huh.NewOption(label, c.peer.Name())
	}
	var sel []string
	err := huh.NewMultiSelect[string]().
		Title("Which machines should join the fleet?").
		Options(opts...).
		Value(&sel).
		Run()
	if err != nil {
		return nil, err
	}
	var picks []tailnet.Peer
	for _, n := range sel {
		picks = append(picks, byName[n].peer)
	}
	return picks, nil
}

func askRoleNotes(name string) (role, notes string) {
	role = "skip"
	opts := append([]string{"skip"}, roles...)
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Role for "+name).Options(huh.NewOptions(opts...)...).Value(&role),
		huh.NewInput().Title("Notes for "+name+" (optional, e.g. sleeps at night)").Value(&notes),
	))
	if err := form.Run(); err != nil || role == "skip" {
		role = ""
	}
	return role, strings.TrimSpace(notes)
}

// selfPath prefers the PATH entry for this binary over its resolved location,
// so a brew upgrade doesn't strand the recorded path in an old Cellar dir.
func selfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "yonder"
	}
	if lp, err := exec.LookPath("yonder"); err == nil {
		a, errA := os.Stat(lp)
		b, errB := os.Stat(exe)
		if errA == nil && errB == nil && os.SameFile(a, b) {
			if abs, err := filepath.Abs(lp); err == nil {
				return abs
			}
		}
	}
	return exe
}
