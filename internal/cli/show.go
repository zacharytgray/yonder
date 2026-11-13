package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/registry"
)

func newShowCmd() *cobra.Command {
	var asJSON, noRefresh bool
	cmd := &cobra.Command{
		Use:   "show <machine>",
		Short: "Show one machine's record and facts",
		Args:  usageArgs(cobra.ExactArgs(1)),
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
			if !noRefresh && a.lazyRefresh(self, []registry.Machine{m}) {
				if m, err = a.machine(args[0]); err != nil {
					return err
				}
			}
			st, stErr := a.status()
			v := view(m, self, st, stErr)
			if asJSON {
				rec := m
				rec.Facts = rec.Facts.Normalize()
				if rec.SSHClientKeys == nil {
					rec.SSHClientKeys = []string{}
				}
				if rec.MeshKeys == nil {
					rec.MeshKeys = []string{}
				}
				v.Record = &rec
				return writeJSON(a.out, v)
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			row := func(k, val string) { fmt.Fprintf(tw, "%s\t%s\n", k, dash(val)) }
			row("name", v.Name)
			row("state", stateText(v))
			row("role", v.Role)
			row("notes", v.Notes)
			f := v.Facts
			osText := strings.Trim(v.OS+"/"+v.Arch, "/")
			if f.OSVersion != "" {
				osText = f.OSVersion + " (" + osText + ")"
			}
			row("os", osText)
			row("tailscale", strings.TrimSpace(v.TailscaleIP+" "+v.DNSName))
			row("ssh", fmt.Sprintf("%s@%s:%d", v.SSHUser, v.TailscaleIP, v.SSHPort))
			row("yonder", m.YonderPath)
			row("summary", v.Summary)
			cpu := f.CPU
			if cpu != "" && f.CPUCores > 0 {
				cpu += fmt.Sprintf(" (%d cores)", f.CPUCores)
			}
			row("cpu", cpu)
			if f.MemoryGB > 0 {
				row("memory", registry.GBText(f.MemoryGB))
			}
			if f.DiskTotalGB > 0 || f.DiskFreeGB > 0 {
				row("disk", fmt.Sprintf("%s free of %s", registry.GBText(f.DiskFreeGB), registry.GBText(f.DiskTotalGB)))
			}
			if f.Laptop {
				row("form", "laptop")
			}
			for _, g := range f.GPUs {
				row("gpu", gpuText(g))
			}
			for _, mo := range f.Models {
				row("model", modelText(mo))
			}
			for _, h := range f.Harnesses {
				row("harness", strings.TrimSpace(h.Name+" "+h.Version+" "+paren(h.Path)))
			}
			disc := "never"
			if !v.DiscoveredAt.IsZero() {
				disc = v.DiscoveredAt.Local().Format(time.DateTime)
				if v.Stale {
					disc += " (stale; run `yonder refresh " + v.Name + "`)"
				}
			}
			row("discovered", disc)
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON (shape documented in docs/design.md)")
	cmd.Flags().BoolVar(&noRefresh, "no-refresh", false, "print stored facts; don't rediscover ones older than a day")
	return cmd
}

func gpuText(g registry.GPU) string {
	t := g.Name
	switch {
	case g.Unified:
		t += " " + registry.GBText(g.MemoryGB) + " (unified)"
	case g.MemoryGB > 0 && g.MemoryUsedGB > 0:
		t += fmt.Sprintf(" %s (%s used, %.0f%% busy)", registry.GBText(g.MemoryGB), registry.GBText(g.MemoryUsedGB), g.UtilPercent)
	case g.MemoryGB > 0:
		t += " " + registry.GBText(g.MemoryGB)
	}
	return t
}

func modelText(m registry.Model) string {
	t := m.Name
	if m.SizeGB > 0 {
		t += " " + registry.GBText(m.SizeGB)
	}
	var tags []string
	if m.Runtime != "" {
		tags = append(tags, m.Runtime)
	}
	if m.Serving {
		tags = append(tags, fmt.Sprintf("serving on :%d", m.Port))
	}
	return strings.TrimSpace(t + " " + paren(strings.Join(tags, ", ")))
}

func paren(s string) string {
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}
