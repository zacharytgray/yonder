package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/doctor"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/transport"
)

func newAgentsBriefingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "briefing",
		Short: "Show and review the briefing about your machines that every agent loads",
		Long: `The briefing is the first part of yonder's block in each harness's instructions file:
a machine list generated from the records (name, role, address, OS, hardware, served
models, availability), then the hand-written agents/` + agents.BriefingFile + ` (routing, where
things live, how secrets are used by name). Every session loads all of it, so it has a
budget: ` + fmt.Sprintf("%d KB rendered (a warning at %d KB), %d KB hand-written", agents.BriefingCap>>10, agents.BriefingWarn>>10, agents.HandCap>>10) + `. Over a cap
nothing renders and nothing is cut.

In ` + agents.BriefingFile + `, write paths as ` + "`<machine>:<path>` or `~/...`" + ` (every machine) so doctor can
check them, and give each section a ` + "`reviewed: YYYY-MM-DD`" + ` line (or one at the top).`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newBriefingShowCmd(), newBriefingReviewCmd())
	return cmd
}

func newBriefingShowCmd() *cobra.Command {
	var harness string
	cmd := &cobra.Command{
		Use:   "show [--harness h]",
		Short: "Print the briefing as a harness gets it",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			self, err := a.selfName()
			if err != nil {
				return err
			}
			h, ok := agents.Lookup(harness)
			if !ok {
				return &usageError{fmt.Errorf("unknown harness %q (want one of %s)", harness, strings.Join(agents.Names(), ", "))}
			}
			text, u, err := agents.NewLinker(self, nil).Briefing(h.Name)
			if err != nil {
				return err
			}
			fmt.Fprint(a.out, text)
			fmt.Fprintf(a.errw, "%s\n", u)
			return nil
		},
	}
	cmd.Flags().StringVar(&harness, "harness", "claude-code", "render it for this harness: "+strings.Join(agents.Names(), ", "))
	return cmd
}

func newBriefingReviewCmd() *cobra.Command {
	var asJSON, draftOnly bool
	cmd := &cobra.Command{
		Use:   "review [--json] [--draft]",
		Short: "List what to fix in the briefing and suggest a trimmed draft",
		Long: `Check the briefing against its budget and the records: paths that don't exist on
their machine (checked read-only, through _exec), machine names that aren't in the
fleet, roles and addresses that contradict a record, sections not reviewed in 90 days,
and lines that your own text in an instructions file repeats. Then print a suggested
draft of agents/` + agents.BriefingFile + ` as a diff. Nothing is changed: edit the file yourself,
or have your agent edit it with you. --draft prints only the draft.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			self, err := a.selfName()
			if err != nil {
				return err
			}
			r, unchecked := a.reviewBriefing(self)
			draft := agents.Draft(r.Source, r.Findings)
			switch {
			case asJSON:
				return writeJSON(a.out, map[string]any{"usage": r.Usage, "findings": r.Findings, "paths": r.Paths, "unchecked": unchecked, "draft": draft})
			case draftOnly:
				fmt.Fprint(a.out, draft)
				return nil
			}
			fmt.Fprintln(a.out, r.Usage)
			if len(r.Findings) == 0 {
				fmt.Fprintln(a.out, "nothing to fix")
			}
			for _, f := range r.Findings {
				fmt.Fprintf(a.out, "%s: %s\n  fix: %s\n", f.Kind, f.Detail, f.Fix)
			}
			for _, u := range unchecked {
				fmt.Fprintf(a.out, "unchecked: %s\n", u)
			}
			if r.Source == "" {
				fmt.Fprintf(a.out, "there is no agents/%s yet; the briefing is the machine list only\n", agents.BriefingFile)
				return nil
			}
			if d := agents.Diff([]byte(r.Source), []byte(draft), agents.BriefingFile, "draft"); d != "" {
				fmt.Fprintf(a.out, "\nsuggested draft (nothing was changed; edit agents/%s yourself):\n%s", agents.BriefingFile, d)
			} else if len(r.Findings) > 0 {
				fmt.Fprintln(a.out, "\nno line can be cut as it is; fix the findings above by hand")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().BoolVar(&draftOnly, "draft", false, "print only the suggested draft")
	return cmd
}

// reviewBriefing is the local review plus the paths checked on their machines.
func (a *app) reviewBriefing(self string) (agents.BriefingReview, []string) {
	r := agents.NewLinker(self, nil).ReviewBriefing(time.Now())
	fs, unchecked := a.checkPaths(self, r.Paths)
	r.Findings = append(r.Findings, fs...)
	return r, unchecked
}

// checkPaths looks for each path on its machine: here with a stat, on the
// others with one read-only _exec each ([ -e ] and nothing else). a ~/ path
// is looked for on every set-up machine. an offline machine is unchecked.
func (a *app) checkPaths(self string, refs []agents.PathRef) ([]agents.Finding, []string) {
	if len(refs) == 0 {
		return nil, nil
	}
	ms, err := a.store.Machines()
	if err != nil {
		return nil, []string{err.Error()}
	}
	on := map[string][]int{} // machine -> refs to check there
	for i, r := range refs {
		for _, m := range ms {
			if (r.Machine == "" && !m.Pending) || r.Machine == m.Name {
				on[m.Name] = append(on[m.Name], i)
			}
		}
	}
	missing := map[int][]string{}
	var unchecked []string
	for _, m := range ms {
		idx := on[m.Name]
		if len(idx) == 0 {
			continue
		}
		var exists []bool
		if m.Name == self {
			for _, i := range idx {
				_, err := os.Stat(registry.ExpandHome(refs[i].Path))
				exists = append(exists, err == nil)
			}
		} else {
			exists, err = a.pathsExist(m, self, refs, idx)
			if err != nil {
				unchecked = append(unchecked, fmt.Sprintf("%d paths on %s (%v)", len(idx), m.Name, firstLine(err.Error())))
				continue
			}
		}
		for k, i := range idx {
			if !exists[k] {
				missing[i] = append(missing[i], m.Name)
			}
		}
	}
	var out []agents.Finding
	keys := make([]int, 0, len(missing))
	for i := range missing {
		keys = append(keys, i)
	}
	sort.Ints(keys)
	for _, i := range keys {
		r := refs[i]
		out = append(out, agents.Finding{Kind: "path", Line: r.Line,
			Detail: fmt.Sprintf("line %d: `%s` doesn't exist on %s", r.Line, r.Written, strings.Join(missing[i], ", ")),
			Fix:    "fix the path, or drop the line if the thing is gone; write `<machine>:<path>` for a path on one machine only"})
	}
	return out, unchecked
}

// pathsExist runs one [ -e ] per path on m. a ~/ path is under that machine's home.
func (a *app) pathsExist(m registry.Machine, self string, refs []agents.PathRef, idx []int) ([]bool, error) {
	var script strings.Builder
	fmt.Fprintf(&script, "printf '%%s\\n' %s\n", transport.Quote(agents.ReplyStart))
	for _, i := range idx {
		p := refs[i].Path
		arg := transport.Quote(p)
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			arg = `"$HOME"/` + transport.Quote(rest)
		}
		fmt.Fprintf(&script, "if [ -e %s ]; then echo y; else echo n; fi\n", arg)
	}
	out, err := a.readOn(m, self, script.String())
	if err != nil {
		return nil, err
	}
	_, reply, ok := strings.Cut(out, agents.ReplyStart+"\n")
	lines := strings.Fields(reply)
	if !ok || len(lines) != len(idx) {
		return nil, fmt.Errorf("unexpected reply from %s", m.Name)
	}
	res := make([]bool, len(idx))
	for k, l := range lines {
		res[k] = l == "y"
	}
	return res, nil
}

// readOn runs a read-only shell script on m through _exec, within 20s.
func (a *app) readOn(m registry.Machine, self, script string) (string, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	old := a.ctx
	a.ctx = ctx
	defer func() { a.ctx = old }()
	var out, errb bytes.Buffer
	code, err := a.execOn(m, self, script, nil, &out, &errb)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", errors.New(orStr(firstLine(errb.String()), fmt.Sprintf("exit %d", code)))
	}
	return out.String(), nil
}

func orStr(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

// briefingChecks are doctor's lines for the briefing.
func (a *app) briefingChecks(self string) []doctor.Check {
	const g = "agents store"
	r, unchecked := a.reviewBriefing(self)
	var out []doctor.Check
	add := func(st doctor.Status, detail, fix string) {
		out = append(out, doctor.Check{Group: g, Name: "briefing", Status: st, Detail: detail, Fix: fix})
	}
	detail := r.Usage.String()
	if len(unchecked) > 0 {
		detail += "; unchecked: " + strings.Join(unchecked, "; ")
	}
	switch {
	case r.Usage.Error != "":
		add(doctor.Fail, r.Usage.Error, "trim agents/"+agents.BriefingFile+"; `yonder agents briefing review` suggests a draft")
	case r.Usage.Bytes > agents.BriefingWarn:
		add(doctor.Warn, detail+": past the warning line", "move detail into a doc or a skill and leave a one-line pointer")
	default:
		add(doctor.Pass, detail, "")
	}
	for _, f := range r.Findings {
		if f.Kind != "budget" {
			add(doctor.Warn, f.Detail, f.Fix)
		}
	}
	return out
}
