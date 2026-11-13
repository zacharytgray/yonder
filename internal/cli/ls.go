package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/tailnet"
)

const staleAfter = 7 * 24 * time.Hour

// machineView is one machine in `ls --json` and `show --json`. agents parse it;
// the shape is documented in docs/design.md, so change both together.
type machineView struct {
	Name         string            `json:"name"`
	Self         bool              `json:"self"`
	State        string            `json:"state"`     // self | online | offline | pending | missing | unknown
	Online       *bool             `json:"online"`    // null when tailscale is unavailable
	LastSeen     *time.Time        `json:"last_seen"` // set only when offline
	Role         string            `json:"role"`
	Notes        string            `json:"notes"`
	OS           string            `json:"os"`
	Arch         string            `json:"arch"`
	TailscaleIP  string            `json:"tailscale_ip"`
	DNSName      string            `json:"dns_name"`
	SSHUser      string            `json:"ssh_user"`
	SSHPort      int               `json:"ssh_port"`
	Hardware     string            `json:"hardware"`
	Summary      string            `json:"summary"` // short, for the ls table
	ModelCount   int               `json:"model_count"`
	Harnesses    []string          `json:"harnesses"`
	Stale        bool              `json:"stale"`
	DiscoveredAt time.Time         `json:"discovered_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Facts        registry.Facts    `json:"facts"`
	Record       *registry.Machine `json:"record,omitempty"` // show only

	pinned bool // has an ssh host key; not in the json
}

type lsOutput struct {
	Self           string          `json:"self"`
	TailscaleError string          `json:"tailscale_error"`
	Machines       []machineView   `json:"machines"`
	Layers         map[string]bool `json:"layers"` // fleet-wide optional layers, on or off here
}

func newLsCmd() *cobra.Command {
	var asJSON, noRefresh bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List fleet machines and whether they are online",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			cfg, err := a.store.Config()
			if err != nil {
				return err
			}
			self := cfg.Self // empty before init; ls still works
			ms, err := a.store.Machines()
			if err != nil {
				return err
			}
			st, stErr := a.status()
			if !noRefresh && a.lazyRefresh(self, ms) {
				if ms, err = a.store.Machines(); err != nil {
					return err
				}
			}
			out := lsOutput{Self: self, Machines: []machineView{}, Layers: a.store.LayersOn()}
			if stErr != nil {
				out.TailscaleError = stErr.Error()
			}
			for _, m := range ms {
				out.Machines = append(out.Machines, view(m, self, st, stErr))
			}
			if asJSON {
				return writeJSON(a.out, out)
			}
			if stErr != nil {
				a.warnf("%v; online state unknown", stErr)
			}
			if len(ms) == 0 {
				fmt.Fprintln(a.out, "no machines yet; run `yonder init`")
				return nil
			}
			if err := printTable(a.out, out.Machines); err != nil {
				return err
			}
			fmt.Fprintln(a.out, layersLine(out.Layers))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON (shape documented in docs/design.md)")
	cmd.Flags().BoolVar(&noRefresh, "no-refresh", false, "print stored facts; don't rediscover ones older than a day")
	return cmd
}

func view(m registry.Machine, self string, st *tailnet.Status, stErr error) machineView {
	f := m.Facts.Normalize()
	v := machineView{
		Name: m.Name, Self: m.Name == self, Role: m.Role, Notes: m.Notes,
		OS: m.OS, Arch: m.Arch, TailscaleIP: m.TailscaleIP, DNSName: m.DNSName,
		SSHUser: m.SSHUser, SSHPort: m.SSHPort,
		Hardware:     hardware(f),
		Summary:      summary(f),
		ModelCount:   len(f.Models),
		Harnesses:    []string{},
		Stale:        !m.Pending && (m.DiscoveredAt.IsZero() || time.Since(m.DiscoveredAt) > staleAfter),
		DiscoveredAt: m.DiscoveredAt, UpdatedAt: m.UpdatedAt,
		Facts: f, pinned: m.SSHHostKey != "",
	}
	for _, h := range f.Harnesses {
		v.Harnesses = append(v.Harnesses, h.Name)
	}
	switch {
	case stErr != nil || st == nil:
		v.State = "unknown"
	default:
		p, ok := st.ForRecord(m.Name, m.DNSName, m.TailscaleIP)
		if !ok {
			v.State = "missing"
			v.Online = boolp(false)
			break
		}
		v.Online = boolp(p.Online)
		if p.Online {
			v.State = "online"
		} else {
			v.State = "offline"
			ls := p.LastSeen
			if !ls.IsZero() {
				v.LastSeen = &ls
			}
		}
	}
	if m.Pending {
		v.State = "pending"
	}
	if v.Self {
		v.State = "self"
		v.Online = boolp(true)
	}
	return v
}

func boolp(b bool) *bool { return &b }

// hardware is a short human summary of the facts.
func hardware(f registry.Facts) string {
	var parts []string
	switch {
	case f.CPU != "" && f.CPUCores > 0:
		parts = append(parts, fmt.Sprintf("%s (%d cores)", f.CPU, f.CPUCores))
	case f.CPU != "":
		parts = append(parts, f.CPU)
	case f.CPUCores > 0:
		parts = append(parts, fmt.Sprintf("%d cores", f.CPUCores))
	}
	if f.MemoryGB > 0 {
		parts = append(parts, fmt.Sprintf("%.0f GB RAM", f.MemoryGB))
	}
	counts := map[string]int{}
	var order []string
	for _, g := range f.GPUs {
		if counts[g.Name] == 0 {
			order = append(order, g.Name)
		}
		counts[g.Name]++
	}
	for _, n := range order {
		if counts[n] > 1 {
			parts = append(parts, fmt.Sprintf("%d× %s", counts[n], n))
		} else {
			parts = append(parts, n)
		}
	}
	if f.DiskFreeGB > 0 {
		parts = append(parts, fmt.Sprintf("%.0f GB free", f.DiskFreeGB))
	}
	return strings.Join(parts, ", ")
}

// summary is the short hardware line for the ls table, e.g. "M3 Pro · 36 GB"
// (apple silicon), "RTX 4090 24 GB · 64 GB" (discrete gpu) or "Intel Core i7 · 64 GB".
func summary(f registry.Facts) string {
	var parts []string
	switch {
	case len(f.GPUs) > 0 && f.GPUs[0].Unified:
		parts = append(parts, registry.ShortGPU(f.GPUs[0].Name))
	case len(f.GPUs) > 0 && f.GPUs[0].MemoryGB >= 2: // skip tiny integrated gpus
		g := registry.ShortGPU(f.GPUs[0].Name) + " " + registry.GBText(f.GPUs[0].MemoryGB)
		if n := len(f.GPUs); n > 1 {
			g = fmt.Sprintf("%d× %s", n, g)
		}
		parts = append(parts, g)
	case f.CPU != "":
		parts = append(parts, registry.ShortCPU(f.CPU))
	case f.CPUCores > 0:
		parts = append(parts, fmt.Sprintf("%d cores", f.CPUCores))
	}
	if f.MemoryGB > 0 {
		parts = append(parts, registry.GBText(f.MemoryGB))
	}
	return strings.Join(parts, " · ")
}

func stateText(v machineView) string {
	switch v.State {
	case "self":
		return "this machine"
	case "offline":
		if v.LastSeen != nil {
			return "offline " + ago(*v.LastSeen)
		}
		return "offline"
	case "missing":
		return "not in tailnet"
	case "pending":
		if v.Online != nil && !*v.Online {
			if v.LastSeen != nil {
				return "pending, offline " + ago(*v.LastSeen)
			}
			return "pending, offline"
		}
	}
	return v.State
}

func printTable(w io.Writer, vs []machineView) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tROLE\tSTATE\tHARDWARE\tMODELS\tHARNESSES")
	stale := false
	for _, v := range vs {
		name := v.Name
		if v.Stale {
			name += "*"
			stale = true
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", name, dash(v.Role), stateText(v), dash(v.Summary), v.ModelCount, dash(strings.Join(v.Harnesses, ",")))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if stale {
		fmt.Fprintln(w, "* facts older than 7 days; run `yonder refresh`")
	}
	for _, v := range vs {
		if v.State == "pending" && v.Online != nil && *v.Online && !v.pinned {
			fmt.Fprintln(w, onlineNowLine(v.Name))
		}
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// layersLine is the one line under the ls table.
func layersLine(on map[string]bool) string {
	var parts []string
	for _, l := range registry.Layers {
		state := "off"
		if on[l.Name] {
			state = "on"
		}
		parts = append(parts, l.Name+" "+state)
	}
	return "layers: " + strings.Join(parts, ", ") + " (yonder layers)"
}
