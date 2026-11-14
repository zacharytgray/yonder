package cli

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/schedule"
)

func newScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule [status|install|remove]",
		Short: "The timer that runs `yonder sync` every 15 minutes",
		Long: `With the agents layer on, every machine runs ` + "`yonder sync`" + ` every 15 minutes, at login
and on wake: a launchd LaunchAgent (` + schedule.Label + `) on macOS, a systemd user timer
(` + schedule.Unit + `.timer) on Linux. Nothing stays running between syncs. init, add and
` + "`yonder enable agents`" + ` install it; ` + "`yonder disable agents`" + ` removes it.

  yonder schedule                  this machine's schedule: loaded, last run, last result
  yonder schedule install [m...]   install it here (or on the machines named); a job with
                                   the same name that yonder didn't write is backed up
                                   and taken over
  yonder schedule remove [m...]    remove it and keep it off there until install

Each run writes its output to ~/.local/state/yonder/sync/schedule.log.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return newApp(cmd).scheduleStatus(false)
		},
	}
	var asJSON bool
	status := &cobra.Command{
		Use:   "status [--json]",
		Short: "Show this machine's sync schedule",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return newApp(cmd).scheduleStatus(asJSON)
		},
	}
	status.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	var allInstall, allRemove bool
	install := &cobra.Command{
		Use:   "install [machine...] [--all-machines]",
		Short: "Install the sync schedule here, or on the machines named",
		Args:  usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp(cmd).scheduleOn("install", args, allInstall)
		},
	}
	install.Flags().BoolVar(&allInstall, "all-machines", false, "every online fleet machine, this one too")
	remove := &cobra.Command{
		Use:   "remove [machine...] [--all-machines]",
		Short: "Remove the sync schedule and keep it off until install",
		Args:  usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp(cmd).scheduleOn("remove", args, allRemove)
		},
	}
	remove.Flags().BoolVar(&allRemove, "all-machines", false, "every online fleet machine, this one too")
	cmd.AddCommand(status, install, remove)
	return cmd
}

func (a *app) scheduleStatus(asJSON bool) error {
	j := schedule.Here("")
	st := schedule.Check(j.OS, j.Home, j.StateDir)
	if asJSON {
		return writeJSON(a.out, st)
	}
	status, detail, fix := st.Problem(time.Now())
	fmt.Fprintf(a.out, "sync schedule: %s\n", detail)
	if st.Installed {
		fmt.Fprintf(a.out, "  job: %s\n", strings.Join(st.Files, ", "))
		if st.Bin != "" {
			fmt.Fprintf(a.out, "  runs: %s sync --scheduled\n", st.Bin)
		}
		fmt.Fprintf(a.out, "  log: %s\n", st.Log)
	}
	if !a.store.LayerOn(registry.LayerAgents) {
		fmt.Fprintln(a.out, "  the agents layer is off, so no schedule is wanted; `yonder enable agents` turns both on")
		return nil
	}
	if status != "pass" {
		fmt.Fprintf(a.out, "  fix: %s\n", fix)
		if status == "fail" {
			return quietExit(ExitError)
		}
	}
	return nil
}

// scheduleOn installs or removes the schedule here and on the named machines.
func (a *app) scheduleOn(sub string, names []string, all bool) error {
	self, err := a.selfName()
	if err != nil {
		return err
	}
	if sub == "install" && !a.store.LayerOn(registry.LayerAgents) {
		return layerOff(registry.LayerAgents, "the sync schedule")
	}
	if all {
		ms, err := a.store.Machines()
		if err != nil {
			return err
		}
		names = nil
		for _, m := range ms {
			names = append(names, m.Name)
		}
	}
	if len(names) == 0 {
		names = []string{self}
	}
	ok := true
	for _, n := range names {
		if _, err := a.machine(n); err != nil {
			return err
		}
		if !a.scheduleOne(self, n, sub, all) {
			ok = false
		}
	}
	if !ok {
		return quietExit(ExitError)
	}
	return nil
}

// scheduleOne runs `schedule install|remove` on one machine: here in
// process, elsewhere through _exec. skipOffline: an offline machine is a
// note, not a failure.
func (a *app) scheduleOne(self, name, sub string, skipOffline bool) bool {
	if name == self {
		var res schedule.Result
		var err error
		if sub == "install" {
			res, err = schedule.Install(schedule.Here(self))
		} else {
			j := schedule.Here(self)
			res, err = schedule.Remove(j.OS, j.Home, j.StateDir, true)
		}
		if err != nil {
			a.warnf("sync schedule here: %v", err)
			return false
		}
		a.printSchedule(res, "")
		return true
	}
	m, err := a.store.Machine(name)
	if err != nil {
		a.warnf("%v", err)
		return false
	}
	var out strings.Builder
	code, err := a.execOn(m, self, yonderCmd(m, "schedule", sub), nil, &out, &out)
	switch {
	case err != nil && ExitCode(err) == ExitTempErr && skipOffline:
		fmt.Fprintf(a.out, "%s: skipped (%v); it gets the schedule at its next sync\n", name, err)
		return true
	case err != nil:
		a.warnf("sync schedule on %s: %v", name, err)
		return false
	case code != 0 && strings.Contains(out.String(), "unknown command"):
		a.warnf("sync schedule on %s: yonder there is too old for `schedule`; upgrade it there, then `yonder schedule install %s`", name, name)
		return false
	}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l != "" {
			fmt.Fprintf(a.out, "%s: %s\n", name, strings.TrimPrefix(l, "yonder: "))
		}
	}
	return code == 0
}

func (a *app) printSchedule(res schedule.Result, prefix string) {
	for _, l := range res.Lines {
		fmt.Fprintln(a.out, prefix+l)
	}
	for _, w := range res.Warn {
		a.warnf("%s", w)
	}
}

// scheduleMachines installs (or, with no, declines) the schedule on the
// machines a setup command just set up. failures are warnings.
func (a *app) scheduleMachines(self string, names []string, no bool) {
	if !a.store.LayerOn(registry.LayerAgents) {
		return
	}
	sub := "install"
	if no {
		sub = "remove"
	}
	for _, n := range names {
		a.scheduleOne(self, n, sub, true)
	}
}

// runScheduled is `yonder sync --scheduled`, what the timer runs: a short
// random wait so machines that fire at once don't find each other busy, then
// a normal sync, with its start and result recorded for doctor.
func (a *app) runScheduled(o syncOpts) error {
	if _, err := a.selfName(); err != nil {
		return err
	}
	if os.Getenv("YONDER_SCHEDULE_JITTER") != "off" {
		time.Sleep(time.Duration(rand.IntN(30)) * time.Second)
	}
	dir := registry.StateDir()
	schedule.RecordStart(dir)
	fmt.Fprintf(a.out, "=== %s scheduled sync\n", time.Now().Format(time.RFC3339))
	err := a.syncCommand("", nil, o)
	code := ExitCode(err)
	var ee *exitError
	quiet := errors.As(err, &ee) && ee.msg == ""
	if err != nil && !quiet {
		fmt.Fprintf(a.out, "yonder: %v\n", err)
	} else {
		err = nil
	}
	schedule.RecordEnd(dir, code, err)
	return quietExit(code)
}

// scheduleHere: a sync schedule job file is here. it only reads the file.
func scheduleHere() bool {
	j := schedule.Here("")
	_, err := os.Stat(schedule.Files(j.OS, j.Home)[0])
	return err == nil
}
