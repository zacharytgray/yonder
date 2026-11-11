package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/transport"
)

func newJobsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "jobs <machine> [id]",
		Short: "List detached jobs on a machine, or print one job's output",
		Args:  usageArgs(cobra.RangeArgs(1, 2)),
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
			if len(args) == 2 {
				code, err := r.Yonder(a.ctx, target(m), []string{"_jobs", args[1]}, transport.IO{Stdout: a.out, Stderr: a.errw})
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
	return cmd
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
