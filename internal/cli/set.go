package cli

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/registry"
)

func newSetCmd() *cobra.Command {
	var c setChange
	var role, notes string
	var here bool
	cmd := &cobra.Command{
		Use:   "set <machine> [--role ROLE] [--notes TEXT]",
		Short: "Change a machine's role or notes",
		Long: `Change a machine's role or notes without adding it again. An empty value
clears it. Facts, keys and addresses are left alone.

A peer must be online: it changes its own record, since sync never lets one
machine change another's, and the registry is then synced to every online
machine.`,
		Args: usageArgs(func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			if cmd.Flags().Changed("role") {
				if role != "" && !slices.Contains(roles, role) {
					return fmt.Errorf("--role must be one of %s, or empty to clear it", strings.Join(roles, ", "))
				}
				c.role = &role
			}
			if cmd.Flags().Changed("notes") {
				notes = strings.TrimSpace(notes)
				c.notes = &notes
			}
			if c.role == nil && c.notes == nil {
				return fmt.Errorf("nothing to set: give --role, --notes or both")
			}
			return nil
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp(cmd).setCommand(args[0], c, here)
		},
	}
	f := cmd.Flags()
	f.StringVar(&role, "role", "", strings.Join(roles, " | ")+` (or "" to clear)`)
	f.StringVar(&notes, "notes", "", `free text shown when the machine is offline (or "" to clear)`)
	// what a peer runs on itself: change this machine's copy only, no sync
	f.BoolVar(&here, "here", false, "")
	_ = f.MarkHidden("here")
	return cmd
}

// setChange holds the fields `set` was given. nil means leave it.
type setChange struct {
	role, notes *string
}

func (c setChange) apply(m *registry.Machine) {
	if c.role != nil {
		m.Role = *c.role
	}
	if c.notes != nil {
		m.Notes = *c.notes
	}
	m.UpdatedAt = time.Now().UTC()
}

// flags passes the change on to the yonder on a peer.
func (c setChange) flags() []string {
	var out []string
	if c.role != nil {
		out = append(out, "--role", *c.role)
	}
	if c.notes != nil {
		out = append(out, "--notes", *c.notes)
	}
	return out
}

func (a *app) setCommand(name string, c setChange, here bool) error {
	self, err := a.selfName()
	if err != nil {
		return err
	}
	m, err := a.machine(name)
	if err != nil {
		return err
	}
	// this machine, or a peer that was never set up (it holds no record of
	// itself yet, and `add` finishes it with these values)
	if here || m.Name == self || m.Pending {
		c.apply(&m)
		if _, err := a.store.MergeIn(registry.Bundle{Machines: []registry.Machine{m}}); err != nil {
			return err
		}
		if !here {
			fmt.Fprintln(a.out, setSummary(m))
			a.syncFleet(self)
		}
		return nil
	}
	// a peer changes its own record; the sync then brings it here and on to the rest
	var out bytes.Buffer
	code, err := a.execOn(m, self, yonderCmd(m, append([]string{"set", m.Name, "--here"}, c.flags()...)...), nil, &out, &out)
	if err != nil {
		return fmt.Errorf("%w; nothing changed", err)
	}
	if tooOld(code, out.String()) {
		return fmt.Errorf("yonder on %s is too old for set; upgrade it there", m.Name)
	}
	if code != 0 {
		_, _ = a.errw.Write(out.Bytes())
		return withCode(ExitError, "%s could not change its record (exit %d)", m.Name, code)
	}
	a.syncFleet(self)
	if got, err := a.store.Machine(m.Name); err == nil {
		m = got
	}
	if (c.role != nil && m.Role != *c.role) || (c.notes != nil && m.Notes != *c.notes) {
		return fmt.Errorf("%s changed its record, but the sync back to this machine didn't bring it; run `yonder refresh %s`", m.Name, m.Name)
	}
	fmt.Fprintln(a.out, setSummary(m))
	return nil
}

func setSummary(m registry.Machine) string {
	return fmt.Sprintf("set %s: role %s, notes %s", m.Name, dashQ(m.Role, false), dashQ(m.Notes, true))
}

func dashQ(s string, quote bool) string {
	if s == "" {
		return "-"
	}
	if quote {
		return fmt.Sprintf("%q", s)
	}
	return s
}
