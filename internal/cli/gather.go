package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
)

func newAgentsGatherCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "gather [--json]",
		Short: "Compare the instructions files on every online machine",
		Long: `Read each online machine's own instructions files (~/.claude/CLAUDE.md,
~/.codex/AGENTS.md, ~/.pi/agent/AGENTS.md), read-only, through _exec. Yonder's block is
left out. Each file is split into sections by heading, or by paragraph where there are no
headings, and each section is reported as the same everywhere, different between
machines, or on some or one machine only.

Yonder never merges the text. To turn differing files into the shared store, ask your
agent to run the reconcile procedure in the yonder skill: it asks you about each
conflict and shows the result as a diff before anything is written. --json has the full
text of every version.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			self, err := a.selfName()
			if err != nil {
				return err
			}
			r, err := a.gather(self)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(a.out, r)
			}
			printGather(a, r)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON, with each version's text")
	return cmd
}

// gather reads this machine's files directly and each online peer's through _exec.
func (a *app) gather(self string) (agents.GatherReport, error) {
	ms, err := a.store.Machines()
	if err != nil {
		return agents.GatherReport{}, err
	}
	var got []agents.MachineFiles
	for _, m := range ms {
		mf := agents.MachineFiles{Machine: m.Name}
		switch {
		case m.Name == self:
			mf.Files = agents.GatherHere(agents.Home())
		case m.Pending:
			mf.Err = "setup unfinished"
		default:
			out, err := a.readOn(m, self, agents.GatherScript())
			if err == nil {
				mf.Files, err = agents.ParseGather(strings.NewReader(out))
			}
			if err != nil {
				mf.Err = firstLine(err.Error())
			}
		}
		got = append(got, mf)
	}
	return agents.Compare(got), nil
}

func printGather(a *app, r agents.GatherReport) {
	for _, m := range r.Machines {
		switch {
		case m.Error != "":
			fmt.Fprintf(a.out, "%s: not read (%s)\n", m.Name, m.Error)
		case len(m.Files) == 0:
			fmt.Fprintf(a.out, "%s: no instructions files\n", m.Name)
		default:
			var fs []string
			for _, f := range m.Files {
				d := fmt.Sprintf("%s %s", f.Path, humanBytes(int64(f.Bytes)))
				if f.Rendered {
					d = f.Path + " (rendered by yonder)"
				} else if f.Yonder {
					d += " outside yonder's block"
				}
				fs = append(fs, d)
			}
			fmt.Fprintf(a.out, "%s: %s\n", m.Name, strings.Join(fs, ", "))
		}
	}
	if len(r.Sections) == 0 {
		fmt.Fprintln(a.out, "no instructions to compare")
		return
	}
	s := r.Summary
	fmt.Fprintf(a.out, "%d sections: %d the same everywhere, %d differ, %d on some machines, %d on one machine only\n\n",
		len(r.Sections), s[agents.SameEverywhere], s[agents.Differs], s[agents.OnSome], s[agents.OnOne])
	for _, sec := range r.Sections {
		where := strings.Join(sec.Machines, ", ")
		if sec.Status == agents.Differs {
			var vs []string
			for _, v := range sec.Versions {
				vs = append(vs, strings.Join(v.Machines, "+"))
			}
			where = strings.Join(vs, " vs ")
		}
		fmt.Fprintf(a.out, "%-8s %-40s %s\n", sec.Status, clipText(orStr(sec.Heading, sec.Key), 40), where)
	}
	if r.Diverged() {
		fmt.Fprintln(a.out, "\n"+reconcileHint)
	}
}

const reconcileHint = "the machines' instructions differ; yonder won't pick one. Ask your agent to run the reconcile procedure in the yonder skill (`yonder agents gather --json` has every version)"

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// warnDiverged prints the reconcile hint when the machines' own instructions
// differ, after turning the agents layer on. it never picks one.
func (a *app) warnDiverged(self string) {
	r, err := a.gather(self)
	if err != nil || !r.Diverged() {
		return
	}
	fmt.Fprintf(a.out, "  %s\n", reconcileHint)
}
