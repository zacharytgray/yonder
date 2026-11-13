package cli

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
)

func newEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <layer>...",
		Short: "Turn a layer on for the whole fleet (" + strings.Join(registry.LayerNames(), ", ") + ")",
		Long: `Turn one or more layers on for every fleet machine. Connecting the machines is always
on; the layers sit on top of it and each one is optional:

` + layerList() + `
The setting syncs with the registry: online machines get it now, offline ones at their
next sync. See which layers are on with ` + "`yonder layers`" + `.`,
		Args: usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp(cmd).setLayers(args, true)
		},
	}
}

func newDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <layer>...",
		Short: "Turn a layer off for the whole fleet; its data is kept",
		Long: `Turn one or more layers off for every fleet machine. Nothing is deleted:
  secrets  --secret is refused with a note saying how to turn it back on; secrets.toml,
           the provider settings and stored tokens stay as they are
  agents   each machine stops syncing its agent config and unlinks it from the harnesses,
           putting back what linking replaced; the store stays in ~/.local/share/yonder/agents
` + "`yonder enable <layer>`" + ` turns it back on.`,
		Args: usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp(cmd).setLayers(args, false)
		},
	}
}

func newLayersCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "layers [--json]",
		Short: "Show which layers are on",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			views, err := a.layerViews()
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(a.out, map[string]any{"layers": views})
			}
			fmt.Fprintln(a.out, "connect  on   always: reach, run on and copy between your machines")
			for _, v := range views {
				state := "off"
				if v.On {
					state = "on"
				}
				detail := v.Summary
				if v.Set {
					detail += fmt.Sprintf(" (set %s by %s)", ago(*v.UpdatedAt), orDash(v.By))
				}
				fmt.Fprintf(a.out, "%-8s %-4s %s\n", v.Name, state, detail)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// layerView is one layer in `yonder layers --json`.
type layerView struct {
	Name      string     `json:"name"`
	On        bool       `json:"on"`
	Set       bool       `json:"set"` // false: nobody has set it, so it uses its default
	By        string     `json:"by"`
	UpdatedAt *time.Time `json:"updated_at"` // null when not set
	Summary   string     `json:"summary"`
}

func (a *app) layerViews() ([]layerView, error) {
	ls, err := a.store.LayerSettings()
	if err != nil {
		return nil, err
	}
	var out []layerView
	for _, l := range registry.Layers {
		v := layerView{Name: l.Name, On: a.store.LayerOn(l.Name), Summary: l.Summary}
		if st, ok := ls[l.Name]; ok {
			t := st.UpdatedAt
			v.Set, v.By, v.UpdatedAt = true, st.By, &t
		}
		out = append(out, v)
	}
	return out, nil
}

func layerList() string {
	s := ""
	for _, l := range registry.Layers {
		s += fmt.Sprintf("  %-8s %s\n", l.Name, l.Summary)
	}
	return s
}

// checkLayerNames refuses names that aren't layers.
func checkLayerNames(names []string) error {
	for _, n := range names {
		if _, ok := registry.LookupLayer(n); !ok {
			return &usageError{fmt.Errorf("no layer named %q (layers: %s)", n, strings.Join(registry.LayerNames(), ", "))}
		}
	}
	return nil
}

func (a *app) setLayers(names []string, on bool) error {
	if err := checkLayerNames(names); err != nil {
		return err
	}
	self, err := a.selfName()
	if err != nil {
		return err
	}
	var changed []string
	for _, n := range names {
		if slices.Contains(changed, n) {
			continue
		}
		if err := a.store.SetLayer(n, on, self); err != nil {
			return err
		}
		changed = append(changed, n)
	}
	reached := a.syncFleet(self)
	a.applyLayers(self)
	if on {
		a.syncStoresQuiet(self)
	}
	word := "off"
	if on {
		word = "on"
	}
	where := "no other machine is online; the others pick it up at their next sync"
	if len(reached) > 0 {
		where = "synced to " + strings.Join(reached, ", ") + "; machines offline now pick it up at their next sync"
	}
	for _, n := range changed {
		fmt.Fprintf(a.out, "%s: %s for the whole fleet\n", n, word)
		fmt.Fprintln(a.out, "  "+layerNext(n, on))
	}
	fmt.Fprintln(a.out, where)
	return nil
}

// layerNext is the one line after turning a layer on or off.
func layerNext(name string, on bool) string {
	switch {
	case name == registry.LayerSecrets && on:
		return "set up a provider where agents use secrets: yonder secret setup"
	case name == registry.LayerSecrets:
		return "--secret is refused until `yonder enable secrets`; secrets.toml and tokens are kept"
	case name == registry.LayerAgents && on:
		return "put agent config in the store with `yonder agents import`, then wire it in with `yonder agents link`"
	case name == registry.LayerAgents:
		return "each machine unlinks its harnesses and puts back what linking replaced; the store is kept"
	}
	return ""
}

// layerOff is the error for using a layer that is turned off.
func layerOff(name, what string) error {
	return fmt.Errorf("the %s layer is off, so %s is refused; turn it on with `yonder enable %s`", name, what, name)
}

// parseLayers reads --layers a,b. "none" means no layers.
func parseLayers(in []string) ([]string, error) {
	var out []string
	for _, n := range in {
		n = strings.TrimSpace(n)
		if n == "" || n == "none" {
			continue
		}
		out = append(out, n)
	}
	return out, checkLayerNames(out)
}

// applyLayers does what this machine's layer settings ask of it here: with
// agents off it unlinks the harnesses; back on, it links them again.
func (a *app) applyLayers(self string) {
	if err := agents.ApplyLayer(a.store.LayerOn(registry.LayerAgents), self, a.out); err != nil {
		a.warnf("agents layer: %v", err)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
