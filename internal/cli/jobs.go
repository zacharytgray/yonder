package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/transport"
)

func newJobsCmd() *cobra.Command {
	var asJSON, follow, kill bool
	cmd := &cobra.Command{
		Use:   "jobs <machine> [id [--follow | --kill]]",
		Short: "List detached jobs on a machine, or print, follow or stop one",
		Long: `List detached jobs on a machine, or print one job's output.

--follow streams the job's output until it finishes, then exits with its code.
Stopping the follow (Ctrl-C) leaves the job running.
--kill stops the job: SIGTERM to its process group, then SIGKILL if anything
is left after 10 seconds. The target logs the kill.`,
		Args: usageArgs(func(cmd *cobra.Command, args []string) error {
			if err := cobra.RangeArgs(1, 2)(cmd, args); err != nil {
				return err
			}
			if follow && kill {
				return fmt.Errorf("pick one of --follow and --kill")
			}
			if (follow || kill) && len(args) != 2 {
				return fmt.Errorf("--follow and --kill need a job id: yonder jobs <machine> <id> --follow")
			}
			if asJSON && len(args) == 2 {
				return fmt.Errorf("--json is for the job list, not one job")
			}
			return nil
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			self, err := a.selfName()
			if err != nil {
				return err
			}
			m, err := a.machine(args[0])
			if err != nil {
				return err
			}
			r, err := a.reach(m, self)
			if err != nil {
				return err
			}
			if kill {
				return a.killJob(r, m, self, args[1])
			}
			if follow {
				return a.followJob(r, m, args[1])
			}
			if len(args) == 2 {
				code, err := r.Yonder(a.ctx, a.target(m), []string{"_jobs", args[1]}, transport.IO{Stdout: a.out, Stderr: a.errw})
				if ue, ok := transport.IsUnreachable(err); ok {
					return unreachableError(m, ue)
				}
				if err != nil {
					return err
				}
				return quietExit(code)
			}
			var out bytes.Buffer
			if err := a.yonderJSON(r, m, []string{"_jobs"}, nil, &out); err != nil {
				if ue, ok := transport.IsUnreachable(err); ok {
					return unreachableError(m, ue)
				}
				return err
			}
			var jobs []remote.JobStatus
			if err := json.Unmarshal(out.Bytes(), &jobs); err != nil {
				return fmt.Errorf("bad _jobs reply: %w", err)
			}
			if asJSON {
				return writeJSON(a.out, jobs)
			}
			if len(jobs) == 0 {
				fmt.Fprintf(a.out, "no jobs on %s\n", m.Name)
				return nil
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATE\tEXIT\tSTARTED\tCOMMAND")
			for _, j := range jobs {
				exit := "-"
				if j.Exit != nil {
					exit = fmt.Sprint(*j.Exit)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", j.ID, j.State, exit, j.StartedAt.Local().Format(time.DateTime), truncate(j.Command, 60))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the job list as JSON")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream the job's output until it ends, then exit with its code")
	cmd.Flags().BoolVar(&kill, "kill", false, "stop the job (SIGTERM, then SIGKILL after 10s)")
	return cmd
}

// followJob streams a job's log until it ends and exits with the job's code.
// yonder's own messages are held until the end, to spot an older target.
func (a *app) followJob(r transport.Runner, m registry.Machine, id string) error {
	var errb bytes.Buffer
	code, err := r.Yonder(a.ctx, a.target(m), []string{"_jobs", id, "--follow"}, transport.IO{Stdout: a.out, Stderr: &errb})
	if ue, ok := transport.IsUnreachable(err); ok {
		return unreachableError(m, ue)
	}
	if err != nil {
		return err
	}
	if code == ExitUsage && strings.Contains(errb.String(), "usage: yonder _jobs [id]") {
		return fmt.Errorf("yonder on %s is too old for jobs --follow; upgrade it there", m.Name)
	}
	_, _ = a.errw.Write(errb.Bytes())
	return quietExit(code)
}

// killJob stops a job through _kill, which logs it on the target.
func (a *app) killJob(r transport.Runner, m registry.Machine, self, id string) error {
	env := remote.Envelope{
		ID: remote.NewID(), Command: "kill job " + id, From: self, To: m.Name, Harness: detectHarness(),
	}
	line, err := env.Encode()
	if err != nil {
		return err
	}
	start := time.Now()
	var errb bytes.Buffer
	code, err := r.Yonder(a.ctx, a.target(m), []string{"_kill", id}, transport.IO{Stdin: bytes.NewReader(line), Stdout: a.out, Stderr: &errb})
	if ue, ok := transport.IsUnreachable(err); ok {
		a.logCaller(env, start, ExitTempErr, ue)
		return unreachableError(m, ue)
	}
	if err != nil {
		a.logCaller(env, start, ExitError, err)
		return err
	}
	if tooOld(code, errb.String()) {
		return fmt.Errorf("yonder on %s is too old for jobs --kill; upgrade it there", m.Name)
	}
	_, _ = a.errw.Write(errb.Bytes())
	return quietExit(code)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
