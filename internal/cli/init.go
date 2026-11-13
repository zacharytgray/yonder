package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/discover"
	"github.com/zacharytgray/yonder/internal/mesh"
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
		idents   []string
		noSkill  bool
		shared   bool
		noMesh   bool
		layers   []string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up this machine and pick tailnet peers to add",
		Long: `Set up this machine as a fleet member, then choose which tailnet peers to add.

Offline peers are saved as pending; ` + "`yonder add <machine>`" + ` finishes one once it is online.
Non-interactive: yonder init --yes --machines gpu-box,laptop [--user me]
If you reach a peer with a specific ssh key: --identity gpu-box=~/.ssh/id_ed25519_gpubox
(repeat the flag per peer; the key path stays on this machine).
With --yes and no --machines, only this machine is set up.
--no-skill leaves every harness's skills folder alone (install later with yonder agents install).
--layers secrets,agents picks the optional layers for the whole fleet (none: connect only);
without it, --yes keeps what is set. ` + "`yonder enable` and `yonder disable`" + ` change them later.
Peers your tailscale account doesn't own (shared in) are listed but can't be picked
unless --allow-shared is given. Two peers with one short name: pass the full dns name.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			interactive := !yes && isTerminal(os.Stdin)
			ids, err := parseIdentities(idents)
			if err != nil {
				return &usageError{err}
			}
			picked, err := parseLayers(layers)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("layers") {
				picked = nil
			} else if picked == nil {
				picked = []string{}
			}
			return a.initCommand(interactive, machines, user, ids, noSkill, shared, noMesh, picked)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "never prompt")
	cmd.Flags().StringSliceVar(&machines, "machines", nil, "peers to add, comma-separated")
	cmd.Flags().StringVar(&user, "user", "", "ssh user on the peers (default: the current user)")
	cmd.Flags().BoolVar(&noSkill, "no-skill", false, "don't install the agent skill on any machine")
	cmd.Flags().BoolVar(&noMesh, "no-mesh", false, "don't add the fleet machines' ssh keys to each other's authorized_keys")
	cmd.Flags().BoolVar(&shared, "allow-shared", false, "allow peers another tailscale account owns (node sharing)")
	cmd.Flags().StringSliceVar(&layers, "layers", nil, "optional layers to turn on for the fleet: "+strings.Join(registry.LayerNames(), ",")+" (or none)")
	cmd.Flags().StringArrayVar(&idents, "identity", nil, "ssh key for a peer, as machine=path (repeatable, kept on this machine only)")
	return cmd
}

// parseIdentities reads repeated machine=path flags.
func parseIdentities(in []string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range in {
		name, path, ok := strings.Cut(kv, "=")
		name, path = strings.TrimSpace(name), strings.TrimSpace(path)
		if !ok || name == "" || path == "" {
			return nil, fmt.Errorf("--identity %q: want machine=path", kv)
		}
		if err := registry.ValidName(name); err != nil {
			return nil, fmt.Errorf("--identity %q: %w", kv, err)
		}
		if err := checkIdentity(path); err != nil {
			return nil, err
		}
		out[name] = path
	}
	return out, nil
}

// layers is nil when --layers wasn't given.
func (a *app) initCommand(interactive bool, chosen []string, user string, ids map[string]string, noSkill, allowShared, noMesh bool, layers []string) error {
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

	if err := a.initLayers(self, interactive, layers); err != nil {
		return err
	}

	candidates := a.initCandidates(st, self, allowShared)
	picks, err := a.pickPeers(st, candidates, chosen, interactive, allowShared)
	if err != nil {
		return err
	}

	for n := range ids {
		if !slices.ContainsFunc(picks, func(p tailnet.Peer) bool { return p.Name() == n }) {
			a.warnf("--identity %s: %s is not one of the peers being added; `yonder ssh-key %s <path>` sets it later", n, n, n)
		}
	}

	var failed, added []string
	for _, p := range picks {
		o := addOpts{user: user, interactive: interactive, identity: ids[p.Name()]}
		if interactive {
			o.role, o.notes = askRoleNotes(p.Name())
		}
		o = a.withDefaults(p.Name(), o)
		if !p.Online {
			if err := a.savePending(p, o); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s is offline (last seen %s); saved as pending, `yonder add %s` finishes setup once it is online\n", p.Name(), ago(p.LastSeen), p.Name())
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
	if a.store.LayerOn(registry.LayerSecrets) {
		if err := a.secretsStep(interactive); err != nil {
			a.warnf("secrets setup: %v", err)
		}
	}
	if !noSkill {
		if err := agents.InstallAll(); err != nil {
			a.warnf("agent skill install: %v", err)
		}
	}

	a.syncFleet(self)
	a.applyLayers(self)
	if len(added) > 0 {
		a.offerMesh(self, "", addOpts{interactive: interactive, allowShared: allowShared, noMesh: noMesh})
	}
	if !noSkill {
		a.installSkillOn(self, added, interactive)
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not add %s; fix the errors above and run `yonder add <machine>`", strings.Join(failed, ", "))
	}
	return nil
}

// initLayers picks the optional layers: from --layers, else a multi-select in
// a terminal, else it keeps what is set. only layers that change are written,
// so an unchanged one keeps its fleet-wide date.
func (a *app) initLayers(self string, interactive bool, layers []string) error {
	cur := a.store.LayersOn()
	want := map[string]bool{}
	switch {
	case layers != nil:
		for _, n := range layers {
			want[n] = true
		}
	case interactive:
		var opts []huh.Option[string]
		for _, l := range registry.Layers {
			opts = append(opts, huh.NewOption(l.Name+"  "+l.Summary, l.Name).Selected(cur[l.Name]))
		}
		var sel []string
		err := huh.NewMultiSelect[string]().
			Title("Which layers do you want? Connecting the machines is always on.").
			Description("Each is optional and fleet-wide. Change them later with `yonder enable` / `yonder disable`.").
			Options(opts...).Value(&sel).Run()
		if err != nil {
			return err
		}
		for _, n := range sel {
			want[n] = true
		}
	default:
		want = cur
	}
	var on []string
	for _, l := range registry.Layers {
		if want[l.Name] != cur[l.Name] {
			if err := a.store.SetLayer(l.Name, want[l.Name], self); err != nil {
				return err
			}
		}
		if want[l.Name] {
			on = append(on, l.Name)
		}
	}
	if len(on) == 0 {
		fmt.Fprintln(a.out, "layers: none on, only connecting machines; `yonder enable <layer>` adds one ("+strings.Join(registry.LayerNames(), ", ")+")")
	} else {
		fmt.Fprintln(a.out, "layers on: "+strings.Join(on, ", "))
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
		SSHHostKey: localHostKey(), MeshKeys: mesh.Own(a.store),
		UpdatedAt: now, DiscoveredAt: now, AddedAt: now, Facts: discover.Local(),
	}
	m.SSHClientKeys = mesh.Union(localClientKeys(), m.MeshKeys)
	if old, err := a.store.Machine(m.Name); err == nil {
		m.Role, m.Notes, m.AddedAt, m.SecretsAccount, m.SecretsWrite = old.Role, old.Notes, old.AddedAt, old.SecretsAccount, old.SecretsWrite
		if old.SSHPort != 0 {
			m.SSHPort = old.SSHPort
		}
	}
	return m
}

type candidate struct {
	peer   tailnet.Peer
	note   string // why it can't be picked, if anything
	shared bool   // another tailscale account owns it
}

func (a *app) initCandidates(st *tailnet.Status, self string, allowShared bool) []candidate {
	var out []candidate
	for _, p := range st.Peers {
		name := p.Name()
		c := candidate{peer: p, shared: !st.Owned(p)}
		switch {
		case name == self:
			continue
		case p.Mobile():
			c.note = p.OS + " device, skipped"
		case registry.ValidName(name) != nil:
			c.note = "name is not kebab-case, skipped"
		case c.shared && !allowShared:
			c.note = "not owned by your tailscale account (" + st.NotOwnedWhy(p) + "); --allow-shared lets you pick it"
		default:
			if old, err := a.store.Machine(name); err == nil && old.DNSName != "" && !strings.EqualFold(old.DNSName, p.DNSName) {
				c.note = "another " + name + " (" + old.DNSName + ") is in the fleet"
			} else if err == nil && !old.Pending {
				c.note = "already in the fleet"
			} else if _, gone := a.store.Tombstone(name); gone {
				c.note = "removed earlier; `yonder add " + name + "` re-adds it"
			}
		}
		out = append(out, c)
	}
	return out
}

// label is a candidate's name for lists: its dns name when another peer shares the short name.
func label(c candidate, cands []candidate) string {
	n := 0
	for _, o := range cands {
		if o.peer.Name() == c.peer.Name() {
			n++
		}
	}
	if n > 1 {
		return c.peer.DNSName
	}
	return c.peer.Name()
}

func (a *app) pickPeers(st *tailnet.Status, cands []candidate, chosen []string, interactive, allowShared bool) ([]tailnet.Peer, error) {
	byDNS := map[string]candidate{}
	var open []candidate
	for _, c := range cands {
		byDNS[c.peer.DNSName] = c
		if c.note == "" {
			open = append(open, c)
		} else if len(chosen) == 0 {
			fmt.Fprintf(a.out, "  %s: %s\n", label(c, cands), c.note)
		}
	}
	if len(chosen) > 0 {
		var picks []tailnet.Peer
		for _, n := range chosen {
			p, err := a.resolvePeer(st, strings.TrimSpace(n), allowShared, interactive)
			if err != nil {
				return nil, err
			}
			if p.DNSName == st.Self.DNSName {
				return nil, fmt.Errorf("%s is this machine", n)
			}
			if p.Mobile() {
				return nil, fmt.Errorf("%s is a %s device; yonder runs on macOS and Linux only", n, p.OS)
			}
			picks = append(picks, p)
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
			names[i] = label(c, cands)
			if c.shared {
				names[i] += " (not yours)"
			}
		}
		fmt.Fprintf(a.out, "peers you can add: %s\npass --machines a,b to add them, or run `yonder add <machine>` later\n", strings.Join(names, ", "))
		return nil, nil
	}
	opts := make([]huh.Option[string], len(open))
	for i, c := range open {
		name := label(c, cands)
		if c.shared {
			name += "  [NOT YOURS: shared from another tailscale account]"
		}
		text := fmt.Sprintf("%s  (%s, online)", name, c.peer.GOOS())
		if !c.peer.Online {
			text = fmt.Sprintf("%s  (%s, offline %s: added as pending)", name, c.peer.GOOS(), ago(c.peer.LastSeen))
		}
		opts[i] = huh.NewOption(text, c.peer.DNSName) // nothing starts selected
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
	for _, d := range sel {
		c := byDNS[d]
		if c.shared && !a.confirm(fmt.Sprintf("%s belongs to another tailscale account. Trust it with ssh and secrets anyway?", d)) {
			continue
		}
		picks = append(picks, c.peer)
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
