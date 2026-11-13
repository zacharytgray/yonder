package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/transport"
)

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Install the yonder skill, and sync agent config between machines",
		Long: `install, uninstall and status manage the yonder skill in each agent harness's user
skills folder:
  claude-code  ~/.claude/skills/yonder
  codex, pi    ~/.agents/skills/yonder
  hermes       ~/.hermes/skills/yonder

With the agents layer on (yonder enable agents), the agents store in
~/.local/share/yonder/agents holds instructions, skills, subagents, commands and hooks,
and syncs between machines: import puts config in, link wires it into the harnesses,
log, diff and restore show and undo changes, accept reviews incoming hooks.
Yonder never edits a harness's settings file.

Store layout:
  briefing.md                  the hand-written part of the briefing about your machines
  render.toml                  each harness's model names for {{model:<tier>}} in the text
  instructions.md              shared instructions for every harness
  instructions.<h>.md          extra text for one harness (claude, codex, pi, hermes)
  skills/<name>/               one folder per skill
  claude/agents/, claude/commands/   Claude Code subagents and commands
  hooks/                       hook scripts, linked into ~/.claude/hooks
  files/<h>/<name>             a file linked into that harness's folder as is
  machines/<machine>/          the same layout, for one machine only`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newAgentsInstallCmd(), newAgentsUninstallCmd(), newAgentsStatusCmd(),
		newAgentsImportCmd(), newAgentsLinkCmd(), newAgentsUnlinkCmd(), newAgentsLogCmd(),
		newAgentsDiffCmd(), newAgentsAcceptCmd(), newAgentsRestoreCmd(), newAgentsAllowCmd(),
		newAgentsBriefingCmd(), newAgentsGatherCmd())
	return cmd
}

func newAgentsInstallCmd() *cobra.Command {
	var (
		harnesses []string
		dryRun    bool
		all       bool
	)
	cmd := &cobra.Command{
		Use:   "install [--harness h]... [--dry-run] [--all-machines]",
		Short: "Copy the skill into every installed harness (or the ones named)",
		Long: `Copy the yonder skill into the skills folder of every agent harness found on this
machine (its binary on PATH, or its config dir). --harness installs for that harness even
if it isn't found. --all-machines also runs ` + "`yonder agents install`" + ` on every online machine.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			home := agents.Home()
			rs, err := agents.Install(agents.Options{Home: home, Harnesses: harnesses, DryRun: dryRun})
			if err != nil {
				return &usageError{err}
			}
			failed := agents.Print(a.out, rs, home)
			if all {
				var args []string
				for _, h := range harnesses {
					args = append(args, "--harness", h)
				}
				if dryRun {
					args = append(args, "--dry-run")
				}
				if !a.agentsOnFleet("install", args) {
					failed = true
				}
			}
			if failed {
				return quietExit(ExitError)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&harnesses, "harness", nil, "harness to install for, even if not found: "+strings.Join(agents.Names(), ", ")+" (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be written, write nothing")
	cmd.Flags().BoolVar(&all, "all-machines", false, "also install on every online fleet machine")
	return cmd
}

func newAgentsUninstallCmd() *cobra.Command {
	var (
		harnesses []string
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "uninstall [--harness h]... [--dry-run]",
		Short: "Remove the skill folder from every harness (or the ones named)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			home := agents.Home()
			rs, err := agents.Uninstall(agents.Options{Home: home, Harnesses: harnesses, DryRun: dryRun})
			if err != nil {
				return &usageError{err}
			}
			if agents.Print(a.out, rs, home) {
				return quietExit(ExitError)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&harnesses, "harness", nil, "harness to uninstall for (repeatable; default all)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be removed, remove nothing")
	return cmd
}

func newAgentsStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status [--json]",
		Short: "Show where the skill is installed, and the agents store",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			home := agents.Home()
			r := agents.Status(home)
			if self, err := a.selfName(); err == nil {
				if ss, err := agents.StoreReport(self); err == nil {
					r.Store = &ss
				}
			}
			if asJSON {
				enc := json.NewEncoder(a.out)
				enc.SetIndent("", "  ")
				return enc.Encode(r)
			}
			fmt.Fprintf(a.out, "yonder skill %s (%s)\n", r.Version, r.SkillHash)
			for _, h := range r.Harnesses {
				state := h.State
				if h.Installed != "" && h.State == agents.StateOutdated {
					state += " (" + h.Installed + ")"
				}
				hint := ""
				switch {
				case !h.Detected && h.State == agents.StateMissing:
					state, hint = "not found", ""
				case h.Detected && h.State != agents.StateCurrent && h.State != agents.StateForeign:
					hint = "  run `yonder agents install`"
				}
				fmt.Fprintf(a.out, "  %-12s %-18s %s%s\n", h.Name, state, agents.Tilde(h.Path, home), hint)
			}
			if r.Store != nil {
				printStoreStatus(a, *r.Store, home)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// agentsOnFleet runs `yonder agents <sub> <args>` on every other machine that is online.
func (a *app) agentsOnFleet(sub string, args []string) bool {
	return a.agentsOn(sub, args, nil)
}

// agentsOn is agentsOnFleet limited to the named machines (all when only is empty).
func (a *app) agentsOn(sub string, args, only []string) bool {
	self, err := a.selfName()
	if err != nil {
		a.warnf("%v", err)
		return false
	}
	ms, err := a.store.Machines()
	if err != nil {
		a.warnf("%v", err)
		return false
	}
	ok := true
	for _, m := range ms {
		if m.Name == self || (len(only) > 0 && !slices.Contains(only, m.Name)) {
			continue
		}
		out, code, err := a.runAgents(m, self, sub, args)
		switch {
		case err != nil && ExitCode(err) == ExitTempErr:
			fmt.Fprintf(a.out, "%s: skipped: %v\n", m.Name, err)
		case err != nil:
			fmt.Fprintf(a.out, "%s: failed: %v\n", m.Name, err)
			ok = false
		default:
			fmt.Fprintf(a.out, "%s:\n", m.Name)
			for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
				fmt.Fprintf(a.out, "  %s\n", l)
			}
			if code != 0 {
				fmt.Fprintf(a.out, "  (exit %d; an older yonder there may not have `agents`; upgrade it)\n", code)
				ok = false
			}
		}
	}
	return ok
}

// runAgents runs `yonder agents <sub> <args>` on m and returns its combined output.
func (a *app) runAgents(m registry.Machine, self, sub string, args []string) (bytes.Buffer, int, error) {
	var out bytes.Buffer
	code, err := a.execOn(m, self, yonderCmd(m, append([]string{"agents", sub}, args...)...), nil, &out, &out)
	return out, code, err
}

// yonderCmd is a shell string that runs m's yonder with args.
func yonderCmd(m registry.Machine, args ...string) string {
	path := m.YonderPath
	if path == "" {
		path = "yonder"
	}
	return transport.Join(append([]string{path}, args...))
}

// execOn runs a shell command on m through _exec, so m logs it like any run.
// offline and unreachable come back as exit-75 errors, logged here.
func (a *app) execOn(m registry.Machine, self, command string, stdin io.Reader, out, errw io.Writer) (int, error) {
	return a.execWith(m, self, command, nil, stdin, out, errw)
}

// execWith is execOn with secret values for the command's environment. they
// travel in the envelope only, and m scrubs them from output and its log.
func (a *app) execWith(m registry.Machine, self, command string, secretVals map[string]string, stdin io.Reader, out, errw io.Writer) (int, error) {
	env := remote.Envelope{
		ID: remote.NewID(), Command: command, From: self, To: m.Name, Harness: detectHarness(),
		Secrets: secretVals,
	}
	line, err := env.Encode()
	if err != nil {
		return ExitError, err
	}
	start := time.Now()
	r, err := a.reach(m, self)
	if err != nil {
		a.logCaller(env, start, ExitTempErr, err)
		return ExitTempErr, err
	}
	in := io.Reader(bytes.NewReader(line))
	if stdin != nil {
		in = io.MultiReader(in, stdin)
	}
	code, err := r.Yonder(a.ctx, a.target(m), []string{"_exec"}, transport.IO{Stdin: in, Stdout: out, Stderr: errw})
	if ue, ok := transport.IsUnreachable(err); ok {
		a.logCaller(env, start, ExitTempErr, ue)
		return ExitTempErr, unreachableError(m, ue)
	}
	if err != nil {
		a.logCaller(env, start, ExitError, err)
		return ExitError, err
	}
	return code, nil
}

func printStoreStatus(a *app, s agents.StoreStatus, home string) {
	if !s.LayerOn {
		fmt.Fprintln(a.out, "agents store: the agents layer is off (yonder enable agents)")
		return
	}
	fmt.Fprintf(a.out, "agents store %s: %d files, %s\n", agents.Tilde(s.Dir, home), s.Files, humanBytes(s.Bytes))
	switch {
	case s.Paused:
		fmt.Fprintln(a.out, "  linked: paused (the layer was off)")
	case len(s.Linked) > 0:
		fmt.Fprintf(a.out, "  linked: %s\n", strings.Join(s.Linked, ", "))
	default:
		fmt.Fprintln(a.out, "  linked: not here (yonder agents link)")
	}
	fmt.Fprintf(a.out, "  review: %s\n", s.Review)
	fmt.Fprintf(a.out, "  %s\n", s.Briefing)
	if s.Briefing.Error != "" {
		fmt.Fprintf(a.out, "  FAIL %s\n", s.Briefing.Error)
	}
	for _, u := range s.Unresolved {
		fmt.Fprintf(a.out, "  unresolved {{model:...}}: %s (agents/%s)\n", u, agents.RenderFile)
	}
	for _, m := range sortedKeys(s.LastSync) {
		fmt.Fprintf(a.out, "  last sync with %s: %s\n", m, ago(s.LastSync[m]))
	}
	for _, p := range s.Pending {
		fmt.Fprintf(a.out, "  waits for review: %s (yonder agents accept)\n", p)
	}
	for _, c := range s.Conflicts {
		fmt.Fprintf(a.out, "  conflict: %s (yonder sync --resolve %s --keep mine|theirs)\n", c, c)
	}
	for _, r := range s.Refused {
		fmt.Fprintf(a.out, "  FAIL %s\n", r)
	}
	for _, p := range s.Problems {
		fmt.Fprintf(a.out, "  link: %s (yonder agents link)\n", p)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
