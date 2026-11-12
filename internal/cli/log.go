package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/runlog"
)

func newLogCmd() *cobra.Command {
	var (
		machine string
		limit   int
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "log [--machine m] [--limit N] [--json]",
		Short: "Show what ran where, merged across every online machine",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			self, err := a.selfName()
			if err != nil {
				return err
			}
			if machine != "" {
				if _, err := a.machine(machine); err != nil {
					return err
				}
			}
			entries, err := runlog.Read(registry.LogPath(), time.Time{})
			if err != nil {
				return err
			}
			peers, err := a.onlinePeers(self)
			if err != nil {
				a.warnf("showing this machine's log only: %v", err)
			}
			for _, m := range peers {
				var out bytes.Buffer
				if err := a.yonderJSON(a.ssh, m, []string{"_log"}, nil, &out); err != nil {
					a.warnf("skipped %s: %v", m.Name, err)
					continue
				}
				sc := bufio.NewScanner(&out)
				sc.Buffer(make([]byte, 64<<10), 4<<20)
				for sc.Scan() {
					var e runlog.Entry
					if json.Unmarshal(sc.Bytes(), &e) == nil {
						entries = append(entries, e)
					}
				}
			}
			entries = filterLog(entries, machine, limit)
			if asJSON {
				return writeJSON(a.out, entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(a.out, "nothing logged yet")
				return nil
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "TIME\tFROM\tTO\tHARNESS\tEXIT\tMS\tCOMMAND")
			for _, e := range entries {
				exit := "-"
				switch {
				case e.Status == runlog.StatusStarted:
					exit = "?"
				case e.Detached && e.Exit == nil:
					exit = "bg"
				case e.Exit != nil:
					exit = fmt.Sprint(*e.Exit)
				}
				to := e.To
				if e.Side == runlog.SideCaller {
					to += " (unreached)"
				}
				cmdText := truncate(e.Command, 60)
				if e.Copy {
					cmdText = truncate(e.Command, 90)
					switch {
					case e.Error != "":
						cmdText += "  (failed: " + truncate(e.Error, 60) + ")"
					case e.Status != runlog.StatusStarted:
						cmdText += fmt.Sprintf("  (%s, %s)", plural(e.Files, "file"), humanBytes(e.Bytes))
					}
				}
				if len(e.Secrets) > 0 {
					cmdText += fmt.Sprintf("  [secrets: %v]", e.Secrets)
				}
				if e.Status == runlog.StatusStarted {
					cmdText += "  (no completion logged: still running, or cut off)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", e.Time.Local().Format(time.DateTime), e.From, to, e.Harness, exit, e.MS, cmdText)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&machine, "machine", "", "only commands to or from this machine")
	cmd.Flags().IntVar(&limit, "limit", 50, "show the newest N entries (0 for all)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON array of log entries")
	return cmd
}

// filterLog pairs start and completion entries, dedupes, filters by machine,
// sorts by time and keeps the newest limit.
func filterLog(es []runlog.Entry, machine string, limit int) []runlog.Entry {
	seen := map[string]bool{}
	out := []runlog.Entry{}
	for _, e := range runlog.Merge(es) {
		key := e.ID + "|" + e.Side + "|" + e.To + "|" + fmt.Sprint(e.Detached) + "|" + e.Status
		if seen[key] {
			continue
		}
		seen[key] = true
		if machine != "" && e.To != machine && e.From != machine {
			continue
		}
		out = append(out, e)
	}
	runlog.Sort(out)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}
