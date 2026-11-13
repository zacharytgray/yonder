package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/store"
)

// backgroundOff stops commands from starting a background sync: tests, and
// YONDER_SYNC_BACKGROUND=off.
var backgroundOff = os.Getenv("YONDER_SYNC_BACKGROUND") == "off"

// acceptTTY and acceptAsk are the terminal check and the question accept asks; tests swap them.
var (
	acceptTTY = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) }
	acceptAsk = func(q string) bool {
		ok := false
		if err := huh.NewConfirm().Title(q).Affirmative("Accept").Negative("Not now").Value(&ok).Run(); err != nil {
			return false
		}
		return ok
	}
)

// pushSoon starts a background sync of the agents store after a change here.
func (a *app) pushSoon() {
	if backgroundOff {
		return
	}
	if err := a.syncBackground(agents.StoreName, nil); err != nil {
		a.warnf("background sync: %v; run `yonder sync`", err)
		return
	}
	fmt.Fprintln(a.out, "syncing to the online machines in the background (yonder sync to watch it)")
}

// agentStore opens the agents store, refusing when the layer is off.
func (a *app) agentStore(what string) (*store.Store, string, error) {
	self, err := a.selfName()
	if err != nil {
		return nil, "", err
	}
	if !a.store.LayerOn(registry.LayerAgents) {
		return nil, "", layerOff(registry.LayerAgents, what)
	}
	s, err := store.Open(agents.StoreName, self)
	if err != nil {
		return nil, "", err
	}
	s.Harness = detectHarness()
	return s, self, nil
}

// withStore runs fn under the store lock, then scans and saves.
func withStore(s *store.Store, fn func() error) ([]string, error) {
	if err := s.Lock(30 * time.Second); err != nil {
		return nil, err
	}
	defer s.Unlock()
	if _, _, err := s.Scan(); err != nil {
		return nil, err
	}
	if err := fn(); err != nil {
		return nil, err
	}
	changed, _, err := s.Scan()
	if err != nil {
		return nil, err
	}
	if err := s.Save(); err != nil {
		return nil, err
	}
	if len(changed) > 0 && s.Kind.AfterChange != nil {
		s.Kind.AfterChange(s, changed)
	}
	return changed, nil
}

func newAgentsLinkCmd() *cobra.Command {
	var (
		harnesses []string
		dryRun    bool
		replace   bool
	)
	cmd := &cobra.Command{
		Use:   "link [--harness h]... [--dry-run] [--replace-links]",
		Short: "Wire the agents store into this machine's harnesses",
		Long: `Wire the agents store into each harness found here (or the ones named). The
instructions start with the briefing about your machines (yonder agents briefing):
  claude-code  the instructions rendered into a marked block in ~/.claude/CLAUDE.md (your
               own text stays), and one link per skill in ~/.claude/skills, per file in
               ~/.claude/agents, commands and hooks
  codex        ~/.codex/AGENTS.md rendered from the store (a marked block if the file is
               your own), skills in ~/.agents/skills, files/codex/* linked into ~/.codex
  pi           ~/.pi/agent/AGENTS.md the same way, skills in ~/.agents/skills
  hermes       a marked block in ~/.hermes/SOUL.md (once hermes has made it) with the
               briefing and instructions.hermes.md (the shared instructions too only with
               [shared] hermes = true in render.toml), and files/hermes/*; it prints how
               to add the skills folder yourself
Settings files are never edited. Anything in the way is backed up first and put back by
` + "`yonder agents unlink`" + `. A symlink in the way (an instructions file or a skills folder
that links elsewhere) is refused unless --replace-links. The instructions are a copy, not
an import: after a sync, or a sync after you edit the store here, the rendered text and the
links follow on their own. Run it again any time.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			_, self, err := a.agentStore("agents link")
			if err != nil {
				return err
			}
			hs, err := pickHarnesses(harnesses)
			if err != nil {
				return &usageError{err}
			}
			if len(hs) == 0 {
				return errors.New("no agent harness found here; name one with --harness")
			}
			l := agents.NewLinker(self, a.out)
			l.DryRun, l.Replace = dryRun, replace
			fmt.Fprintf(a.out, "linking %s\n", strings.Join(hs, ", "))
			if err := l.Link(hs); err != nil {
				return err
			}
			if !dryRun {
				fmt.Fprintln(a.out, "to sync at the start of each session, have the harness run `yonder sync --background` (for Claude Code, a SessionStart hook)")
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&harnesses, "harness", nil, "harness to link, even if not found: "+strings.Join(agents.Names(), ", ")+" (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change, change nothing")
	cmd.Flags().BoolVar(&replace, "replace-links", false, "replace symlinks in the way with real files and folders (unlink puts them back)")
	return cmd
}

// pickHarnesses is the named harnesses, or the ones found here.
func pickHarnesses(names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		h, ok := agents.Lookup(n)
		if !ok {
			return nil, fmt.Errorf("unknown harness %q (want one of %s)", n, strings.Join(agents.Names(), ", "))
		}
		if !slices.Contains(out, h.Name) {
			out = append(out, h.Name)
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	for _, h := range agents.Harnesses {
		if ok, _ := h.Detect(agents.Home()); ok {
			out = append(out, h.Name)
		}
	}
	return out, nil
}

func newAgentsUnlinkCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "unlink [--dry-run]",
		Short: "Undo agents link here and put back what it replaced",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			self, err := a.selfName()
			if err != nil {
				return err
			}
			l := agents.NewLinker(self, a.out)
			l.DryRun = dryRun
			st, err := l.State()
			if err != nil {
				return err
			}
			if len(st.Entries) == 0 {
				fmt.Fprintln(a.out, "nothing is linked here")
				if !dryRun {
					return l.Unlink(false)
				}
				return nil
			}
			return l.Unlink(false)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change, change nothing")
	return cmd
}

func newAgentsImportCmd() *cobra.Command {
	var o agents.ImportOptions
	cmd := &cobra.Command{
		Use:   "import <path> [--kind k]... [--harness h] [--machine m] [--replace] [--dry-run]",
		Short: "Copy agent config into the store",
		Long: `Copy agent config into the agents store, which then syncs to the fleet. Only these
kinds go in: ` + strings.Join(agents.Kinds, ", ") + `. Settings, auth and credential files
never do, and files that look like secrets are refused.

  yonder agents import ~/.claude               CLAUDE.md, skills/, agents/, commands/, hooks/
  yonder agents import ~/notes/rules.md --kind instructions
  yonder agents import ~/.codex/AGENTS.md --kind instructions --harness codex   (an overlay)
  yonder agents import ./my-skill                   one folder with a SKILL.md
  yonder agents import ~/.codex/hooks.json --kind files --harness codex
  yonder agents import ./gpu-skills --kind skills --machine gpu-box   (that machine only)

A store file that differs is left alone unless --replace (the old one is kept).`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			s, _, err := a.agentStore("agents import")
			if err != nil {
				return err
			}
			o.Src = registry.ExpandHome(args[0])
			if o.Machine != "" {
				if err := registry.ValidName(o.Machine); err != nil {
					return &usageError{err}
				}
			}
			plan, err := agents.PlanImport(o)
			if err != nil {
				return &usageError{err}
			}
			var rs []agents.ImportResult
			_, err = withStore(s, func() error {
				rs = agents.Import(s, plan, o.Replace, o.DryRun)
				return nil
			})
			if err != nil {
				return err
			}
			failed := false
			counts := map[string]int{}
			for _, r := range rs {
				counts[r.Action]++
				switch {
				case r.Refused:
					failed = true
					fmt.Fprintf(a.out, "FAIL %s: %s\n", r.Rel, r.Detail)
				case r.Action == "exists":
					fmt.Fprintf(a.out, "left %s: %s\n", r.Rel, r.Detail)
				case r.Action != "unchanged":
					verb := r.Action
					if o.DryRun {
						verb = "would be " + verb
					}
					fmt.Fprintf(a.out, "%s %s\n", verb, r.Rel)
				}
			}
			for _, sk := range plan.Skipped {
				fmt.Fprintf(a.out, "skipped %s\n", sk)
			}
			fmt.Fprintf(a.out, "%d added, %d replaced, %d unchanged, %d left as they are, %d refused\n",
				counts["added"], counts["replaced"], counts["unchanged"], counts["exists"], counts["refused"]+counts["failed"])
			if !o.DryRun && counts["added"]+counts["replaced"] > 0 {
				a.pushSoon()
			}
			if failed {
				return quietExit(ExitError)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&o.Kinds, "kind", nil, "what to take: "+strings.Join(agents.Kinds, ", ")+" (repeatable; default: whatever is there)")
	cmd.Flags().StringVar(&o.Harness, "harness", "", "instructions become this harness's overlay; files belong to it")
	cmd.Flags().StringVar(&o.Machine, "machine", "", "put it under machines/<name>/, for that machine only")
	cmd.Flags().BoolVar(&o.Replace, "replace", false, "overwrite store files that differ (the old ones are kept)")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "show what would go in, change nothing")
	return cmd
}

func newAgentsLogCmd() *cobra.Command {
	var (
		asJSON bool
		limit  int
	)
	cmd := &cobra.Command{
		Use:   "log [path] [-n N] [--json]",
		Short: "Show which machine changed what in the agents store",
		Long: `Show the changes this machine recorded in the agents store, newest last: its own
edits, found when a sync scans, and every change it received, with the machine (and
harness, when yonder knows it) the change came from.`,
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			self, err := a.selfName()
			if err != nil {
				return err
			}
			s, err := store.Open(agents.StoreName, self)
			if err != nil {
				return err
			}
			rel := ""
			if len(args) == 1 {
				rel = storeRel(s, args[0])
			}
			es, err := s.Log(rel)
			if err != nil {
				return err
			}
			if limit > 0 && len(es) > limit {
				es = es[len(es)-limit:]
			}
			if asJSON {
				if es == nil {
					es = []store.LogEntry{}
				}
				return writeJSON(a.out, es)
			}
			if len(es) == 0 {
				fmt.Fprintln(a.out, "no changes recorded yet")
			}
			for _, e := range es {
				via := ""
				if e.From != "" && e.From != e.Origin.Machine {
					via = " (through " + e.From + ")"
				}
				h := ""
				if e.Origin.Harness != "" {
					h = " via " + e.Origin.Harness
				}
				fmt.Fprintf(a.out, "%s  %-8s %s  by %s%s%s\n", e.Origin.Time.Local().Format("2006-01-02 15:04"), e.Action, e.Path, orDash(e.Origin.Machine), h, via)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "show the last N (0 for all)")
	return cmd
}

func newAgentsDiffCmd() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:   "diff <path> [--version N]",
		Short: "Show what the last change to a store file changed",
		Long: `Show the difference between a store file now and the version it replaced (or
--version N, from ` + "`yonder agents restore <path> --list`" + `). For a change that waits for
review, it shows what accepting would change.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			self, err := a.selfName()
			if err != nil {
				return err
			}
			s, err := store.Open(agents.StoreName, self)
			if err != nil {
				return err
			}
			rel := storeRel(s, args[0])
			if live, in, err := s.PendingData(rel); err == nil {
				items, _ := s.PendingList()
				fmt.Fprintf(a.out, "%s waits for review here (yonder agents accept %s)\n", rel, rel)
				for _, it := range items {
					if it.Path == rel {
						fmt.Fprint(a.out, pendingDiff(it, live, in))
					}
				}
				return nil
			}
			vs, err := s.History(rel)
			if err != nil {
				return err
			}
			if len(vs) == 0 {
				return fmt.Errorf("no earlier version of %s is kept here", rel)
			}
			if n < 1 || n > len(vs) {
				return &usageError{fmt.Errorf("%s has versions 1 to %d", rel, len(vs))}
			}
			old, err := vs[n-1].Data()
			if err != nil {
				return err
			}
			cur, _ := s.Live(rel)
			st, _ := s.State()
			o := st.Files[rel].Origin
			fmt.Fprintf(a.out, "%s: now %s; was version %d (%s)\n", rel, originText(o), n, vs[n-1].Why)
			fmt.Fprint(a.out, orNoDiff(agents.Diff(old, cur, "version "+fmt.Sprint(n), "now")))
			return nil
		},
	}
	cmd.Flags().IntVar(&n, "version", 1, "compare with this kept version (1 is the most recent)")
	return cmd
}

// pendingDiff shows a waiting change: the exec bit, then the text.
func pendingDiff(it store.PendingItem, live, in []byte) string {
	mode := agents.ModeChange(it.Pending.LiveExists && it.Pending.LiveExec, it.State.Exec)
	d := agents.Diff(live, in, "live", "incoming")
	if mode == "" {
		return orNoDiff(d)
	}
	return mode + d
}

func orNoDiff(d string) string {
	if d == "" {
		return "no difference\n"
	}
	return d
}

func newAgentsAcceptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accept [path...]",
		Short: "Review incoming hooks and other executables, and make them live",
		Long: `Hooks run code, so a new or changed one from another machine waits here until you
accept it: it syncs on to the fleet, but nothing runs it on this machine yet. accept shows
each change (all, or the paths named) and asks. It needs a terminal, so an agent can't
accept for you.

Which changes wait is [sync] review in ~/.config/yonder/config.toml: "executables" (the
default: hooks, executable files, hook configs), "all", or "none".`,
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			s, _, err := a.agentStore("agents accept")
			if err != nil {
				return err
			}
			items, err := s.PendingList()
			if err != nil {
				return err
			}
			s.Unlock()
			if len(args) > 0 {
				items = slices.DeleteFunc(items, func(it store.PendingItem) bool {
					return !slices.Contains(args, it.Path) && !slices.Contains(args, storeRel(s, it.Path))
				})
			}
			if len(items) == 0 {
				fmt.Fprintln(a.out, "nothing waits for review")
				return nil
			}
			if !acceptTTY() {
				for _, it := range items {
					fmt.Fprintf(a.out, "waiting: %s (%s)\n", agents.Printable(it.Path), originText(it.State.Origin))
				}
				return errors.New("accept needs a person at a terminal; run `yonder agents accept` there (yonder agents diff <path> shows a change)")
			}
			accepted := 0
			_, err = withStore(s, func() error {
				for _, it := range items {
					live, in, err := s.PendingData(it.Path)
					if err != nil {
						fmt.Fprintf(a.out, "%s: %v\n", agents.Printable(it.Path), err)
						continue
					}
					fmt.Fprintf(a.out, "\n%s (%s)\n", agents.Printable(it.Path), originText(it.State.Origin))
					fmt.Fprint(a.out, pendingDiff(it, live, in))
					if !acceptAsk("Make " + agents.Printable(it.Path) + " live on this machine?") {
						fmt.Fprintf(a.out, "left %s waiting\n", agents.Printable(it.Path))
						continue
					}
					if err := s.Accept(it.Path); err != nil {
						fmt.Fprintf(a.out, "%s: %v\n", agents.Printable(it.Path), err)
						continue
					}
					accepted++
					fmt.Fprintf(a.out, "accepted %s\n", agents.Printable(it.Path))
				}
				return nil
			})
			if err != nil {
				return err
			}
			if accepted > 0 {
				a.relinkHere()
			}
			return nil
		},
	}
	return cmd
}

// relinkHere refreshes this machine's links after a local change made files live.
func (a *app) relinkHere() {
	self, err := a.selfName()
	if err != nil {
		return
	}
	// a no-op unless linked and not paused, checked under the lock
	if err := agents.NewLinker(self, a.out).Link(nil); err != nil {
		a.warnf("%v", err)
	}
}

func newAgentsRestoreCmd() *cobra.Command {
	var (
		n    int
		list bool
	)
	cmd := &cobra.Command{
		Use:   "restore <path> [--version N | --list]",
		Short: "Put back an earlier version of a store file",
		Long: `Put back a version that a sync, import, resolve or accept replaced or deleted. The
last 10 per file are kept here, for 30 days. --list shows them; the default is the most
recent. The restore then syncs to the fleet like any edit here.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			s, _, err := a.agentStore("agents restore")
			if err != nil {
				return err
			}
			rel := storeRel(s, args[0])
			if list {
				vs, err := s.History(rel)
				if err != nil {
					return err
				}
				if len(vs) == 0 {
					fmt.Fprintf(a.out, "no earlier version of %s is kept here\n", rel)
				}
				for _, v := range vs {
					fmt.Fprintf(a.out, "%d  %s  %s (%s)\n", v.N, idTime(v.ID), v.Why, originText(v.State.Origin))
				}
				return nil
			}
			var v store.Version
			var pending bool
			_, err = withStore(s, func() error {
				var err error
				v, pending, err = s.Restore(rel, n)
				return err
			})
			if err != nil {
				return err
			}
			if pending {
				fmt.Fprintf(a.out, "restored %s to version %d (%s); hooks run code, so it waits for `yonder agents accept` here\n", rel, v.N, v.Why)
			} else {
				fmt.Fprintf(a.out, "restored %s to version %d (%s)\n", rel, v.N, v.Why)
			}
			a.pushSoon()
			return nil
		},
	}
	cmd.Flags().IntVar(&n, "version", 1, "which kept version (1 is the most recent)")
	cmd.Flags().BoolVar(&list, "list", false, "list the kept versions")
	return cmd
}

func idTime(id string) string {
	ns, _ := strconv.ParseInt(id, 10, 64)
	return time.Unix(0, ns).Local().Format("2006-01-02 15:04")
}

func newAgentsAllowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "allow <path>",
		Short: "Let a store file sync though it looks like it holds a secret",
		Long: `The secret check catches common key formats, and now and then a file that only looks
like one. allow lets that file's current content sync; a later change is checked again.
Names like .env or *.pem never sync, allowed or not.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			s, _, err := a.agentStore("agents allow")
			if err != nil {
				return err
			}
			rel := storeRel(s, args[0])
			if _, err := withStore(s, func() error { return s.Allow(rel) }); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "allowed %s as it is now\n", rel)
			a.pushSoon()
			return nil
		},
	}
}
