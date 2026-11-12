package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/mesh"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/transport"
)

func newMeshCmd() *cobra.Command {
	var yes, allowShared bool
	cmd := &cobra.Command{
		Use:   "mesh [machine] [--yes]",
		Short: "Let every fleet machine ssh into every other one",
		Long: `Let every fleet machine ssh into every other one. yonder adds each machine's ssh
client keys on record to every other online machine's ~/.ssh/authorized_keys.

It only appends: lines already there stay as they are, a key already there (even
commented out) is skipped, and the file is backed up first
(authorized_keys.yonder-bak.<time>). Each added line ends in "yonder:<machine>", so
` + "`yonder remove`" + ` can take it out again. A symlinked authorized_keys is never rewritten;
yonder prints the lines to add by hand.

With a machine, only the keys to and from that machine. ` + "`yonder add`" + ` offers this
for the machine it just added. Keys come only from set-up records this machine
holds, never from a removed machine, and only from machines your tailscale account
owns unless --allow-shared.`,
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			focus := ""
			if len(args) == 1 {
				focus = args[0]
				if err := registry.ValidName(focus); err != nil {
					return &usageError{err}
				}
				if _, err := a.machine(focus); err != nil {
					return err
				}
			}
			self, err := a.selfName()
			if err != nil {
				return err
			}
			return a.mesh(self, focus, yes || !promptable(), allowShared)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask for confirmation")
	cmd.Flags().BoolVar(&allowShared, "allow-shared", false, "also hand out keys of machines another tailscale account owns")
	return cmd
}

type meshStep struct {
	m             registry.Machine
	added, pruned []string
}

// mesh brings each online machine's authorized_keys in line with the grant
// set: it appends the fleet's missing mesh keys and prunes lines it tagged
// earlier that are no longer granted. focus limits it to the keys to and from
// one machine. without yes it asks first, defaulting to yes.
func (a *app) mesh(self, focus string, yes, allowShared bool) error {
	if _, err := a.status(); err != nil {
		return withCode(ExitTempErr, "mesh needs tailscale status: %v", err)
	}
	a.syncOwnMeshKeys(self)
	b, err := a.store.Bundle()
	if err != nil {
		return err
	}
	src, skips := mesh.Sources(self, b.Machines, b.Tombstones, a.st, allowShared)
	for _, s := range skips {
		if (focus == "" || s.Name == focus) && s.Why != "it was removed" {
			a.warnf("mesh: not handing out %s's keys: %s", s.Name, s.Why)
		}
	}

	var targets, offline []registry.Machine
	for _, m := range b.Machines {
		switch {
		case m.Name == self:
			targets = append(targets, m)
		case m.Pending || m.SSHHostKey == "":
		case a.isOnline(m):
			targets = append(targets, m)
		default:
			offline = append(offline, m)
		}
	}

	var steps []meshStep
	var failed []string
	for _, m := range targets {
		content, err := a.readKeys(m, self)
		if err != nil {
			a.warnf("mesh: %s: %v", m.Name, err)
			failed = append(failed, m.Name)
			continue
		}
		if _, added, pruned := mesh.Plan(content, m.Name, focus, src, skips); len(added)+len(pruned) > 0 {
			steps = append(steps, meshStep{m, added, pruned})
		}
	}
	var later []string
	for _, m := range offline {
		if focus == "" || m.Name == focus || len(mesh.Grants(m.Name, focus, src)) > 0 {
			later = append(later, m.Name)
		}
	}
	defer func() {
		if len(later) > 0 {
			fmt.Fprintf(a.out, "offline, so not changed: %s. Run `yonder mesh` once they're online.\n", strings.Join(later, ", "))
		}
	}()

	if len(steps) == 0 {
		if len(failed) == 0 {
			fmt.Fprintln(a.out, "mesh: every online machine already trusts exactly the fleet's mesh keys")
			return nil
		}
		return fmt.Errorf("mesh: could not check %s", strings.Join(failed, ", "))
	}
	fmt.Fprintln(a.out, "This will change ~/.ssh/authorized_keys (backup first; only lines tagged yonder:<machine> are removed):")
	for _, s := range steps {
		var parts []string
		if len(s.added) > 0 {
			parts = append(parts, "add keys of "+linesSummary(s.added))
		}
		if len(s.pruned) > 0 {
			parts = append(parts, "remove keys no longer granted of "+linesSummary(s.pruned))
		}
		fmt.Fprintf(a.out, "  - on %s: %s\n", s.m.Name, strings.Join(parts, "; "))
	}
	if !yes && !a.confirm("Let these machines ssh into each other?") {
		fmt.Fprintf(a.out, "skipped; `yonder mesh%s` does it later\n", focusArg(focus))
		return nil
	}

	for _, s := range steps {
		added, pruned, backup, err := a.applyMesh(s.m, self, focus, src, skips)
		switch {
		case err != nil:
			a.warnf("mesh: %s: %v", s.m.Name, err)
			failed = append(failed, s.m.Name)
		case len(added)+len(pruned) == 0:
			fmt.Fprintf(a.out, "%s: nothing to change\n", s.m.Name)
		default:
			var parts []string
			if len(added) > 0 {
				parts = append(parts, fmt.Sprintf("added %d key(s) of %s", len(added), linesSummary(added)))
			}
			if len(pruned) > 0 {
				parts = append(parts, fmt.Sprintf("removed %d line(s) no longer granted, of %s", len(pruned), linesSummary(pruned)))
			}
			fmt.Fprintf(a.out, "%s: %s (backup: %s)\n", s.m.Name, strings.Join(parts, "; "), agents.Tilde(backup, homeFor(s.m, self)))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("mesh: could not update %s; fix the errors above, then `yonder mesh%s`", strings.Join(failed, ", "), focusArg(focus))
	}
	return nil
}

// syncOwnMeshKeys keeps this machine's own record in step with its keys, and
// reports whether they changed.
func (a *app) syncOwnMeshKeys(self string) bool {
	m, err := a.store.Machine(self)
	if err != nil {
		return false
	}
	keys := mesh.Own(a.store)
	if slices.Equal(keys, m.MeshKeys) {
		return false
	}
	m.MeshKeys, m.SSHClientKeys = keys, mesh.Union(m.SSHClientKeys, keys)
	m.UpdatedAt = time.Now().UTC()
	if err := a.store.SaveMachine(m); err != nil {
		a.warnf("%v", err)
		return false
	}
	return true
}

func focusArg(focus string) string {
	if focus == "" {
		return ""
	}
	return " " + focus
}

func homeFor(m registry.Machine, self string) string {
	if m.Name == self {
		return registry.UserHome()
	}
	return "" // a peer's backup path is printed as is
}

func linesSummary(lines []string) string {
	var names []string
	for _, l := range lines {
		if n := mesh.TagOf(l); n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

// readKeys reads m's authorized_keys: the file here for self, sh over ssh for a peer.
func (a *app) readKeys(m registry.Machine, self string) (string, error) {
	if m.Name == self {
		data, err := os.ReadFile(transport.AuthorizedKeysPath(registry.UserHome()))
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return string(data), err
	}
	if _, err := a.pinned(m); err != nil {
		return "", err
	}
	f, err := mesh.Read(a.ctx, a.ssh, a.target(m), "")
	return f.Content, err
}

// applyMesh plans and writes m's authorized_keys against what is there now.
// a symlinked file isn't written: the error lists each line to add (+) or delete (-).
func (a *app) applyMesh(m registry.Machine, self, focus string, src []mesh.Source, skips []mesh.Skip) (added, pruned []string, backup string, err error) {
	edit := func(c string) (string, []string) {
		var out string
		out, added, pruned = mesh.Plan(c, m.Name, focus, src, skips)
		var lines []string
		for _, l := range added {
			lines = append(lines, "+ "+l)
		}
		for _, l := range pruned {
			lines = append(lines, "- "+l)
		}
		return out, lines
	}
	if m.Name == self {
		_, backup, err = transport.EditAuthorizedKeys(transport.AuthorizedKeysPath(registry.UserHome()), true, edit)
	} else if _, err = a.pinned(m); err == nil {
		_, backup, err = mesh.Edit(a.ctx, a.ssh, a.target(m), "", true, edit)
	}
	if err != nil {
		return nil, nil, "", err
	}
	return added, pruned, backup, nil
}

// stripMesh drops the lines mesh added for name on m, keeping keys in keep.
func (a *app) stripMesh(m registry.Machine, self, name string, keep map[string]bool) ([]string, string, error) {
	edit := func(c string) (string, []string) { return mesh.StripTag(c, name, keep) }
	if m.Name == self {
		return transport.EditAuthorizedKeys(transport.AuthorizedKeysPath(registry.UserHome()), false, edit)
	}
	if _, err := a.pinned(m); err != nil {
		return nil, "", err
	}
	return mesh.Edit(a.ctx, a.ssh, a.target(m), "", false, edit)
}
