package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/doctor"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/secrets"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
	"github.com/zacharytgray/yonder/internal/version"
)

func newDoctorCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor [--json]",
		Short: "Check every piece of the fleet and print fixes",
		Long: `Check tailscale, this machine's setup, every fleet machine (online, ssh with the
pinned host key, yonder version, stale or pending records), registry consistency, the log
dir, the agent skill and secrets. Each line is PASS, WARN or FAIL, with a fix under it.
Offline machines are a WARN. Exits 1 if anything failed.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			r := a.doctor()
			if asJSON {
				enc := json.NewEncoder(a.out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(r); err != nil {
					return err
				}
			} else {
				doctor.Render(a.out, r, colorOK(a))
			}
			return quietExit(r.ExitCode())
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func colorOK(a *app) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := a.out.(*os.File)
	return ok && isTerminal(f)
}

func (a *app) doctor() doctor.Report {
	cfg, cfgErr := a.store.Config()
	st, stErr := a.status()
	sec := []secrets.Check{{Name: "layer", OK: true, Detail: "off; `yonder enable secrets` turns it on"}}
	if a.store.LayerOn(registry.LayerSecrets) {
		sec = secrets.Health()
	}
	e := doctor.Env{
		Self:         cfg.Self,
		Store:        a.store,
		Status:       st,
		StatusErr:    stErr,
		Fixture:      os.Getenv("YONDER_TAILSCALE_JSON"),
		TailscaleBin: tailnet.Binary,
		Version:      version.Version,
		Home:         doctor.LocalHome(),
		StateDir:     registry.StateDir(),
		Secrets:      sec,
		Layers:       a.store.LayersOn(),
		Remote:       doctorRemote{a},
		Now:          time.Now(),
	}
	checks := doctor.Run(e)
	if cfg.Self != "" && a.store.LayerOn(registry.LayerAgents) {
		checks = append(checks, agentsChecks(cfg.Self)...)
	}
	if cfgErr != nil {
		checks = append([]doctor.Check{{Group: "this machine", Name: "config", Status: doctor.Fail,
			Detail: cfgErr.Error(), Fix: "fix or delete the file named above, then `yonder init`"}}, checks...)
	}
	return doctor.NewReport(cfg.Self, version.Version, checks)
}

// doctorRemote runs doctor's reads over ssh, each bounded so one hung machine can't stall the run.
type doctorRemote struct{ a *app }

func (d doctorRemote) yonder(m registry.Machine, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(d.a.ctx, 20*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	code, err := d.a.ssh.Yonder(ctx, d.a.target(m), args, transport.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, &doctor.YonderError{Code: code, Stderr: firstLine(errb.String())}
	}
	return out.Bytes(), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if l, _, ok := strings.Cut(s, "\n"); ok {
		return l
	}
	return s
}

func (d doctorRemote) Version(m registry.Machine) (string, error) {
	out, err := d.yonder(m, []string{"version"})
	if err != nil {
		return "", err
	}
	// "yonder <version> (<os>/<arch>)"
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return "", fmt.Errorf("unexpected `yonder version` output %q", strings.TrimSpace(string(out)))
	}
	return f[1], nil
}

// Registry sends an empty bundle to _sync: it merges nothing and returns what the machine has.
func (d doctorRemote) Registry(m registry.Machine) (registry.Bundle, error) {
	ctx, cancel := context.WithTimeout(d.a.ctx, 20*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	code, err := d.a.ssh.Yonder(ctx, d.a.target(m), []string{"_sync"}, transport.IO{
		Stdin: strings.NewReader(`{"machines":[],"tombstones":[]}`), Stdout: &out, Stderr: &errb,
	})
	if err != nil {
		return registry.Bundle{}, err
	}
	if code != 0 {
		return registry.Bundle{}, &doctor.YonderError{Code: code, Stderr: firstLine(errb.String())}
	}
	var b registry.Bundle
	if err := json.Unmarshal(out.Bytes(), &b); err != nil {
		return registry.Bundle{}, fmt.Errorf("bad _sync reply: %w", err)
	}
	return b, nil
}

func (d doctorRemote) Skills(m registry.Machine) (agents.Report, error) {
	out, err := d.yonder(m, []string{"agents", "status", "--json"})
	if err != nil {
		return agents.Report{}, err
	}
	var r agents.Report
	if err := json.Unmarshal(out, &r); err != nil {
		return agents.Report{}, fmt.Errorf("bad `agents status` reply: %w", err)
	}
	return r, nil
}

func (d doctorRemote) AuthorizedKeys(m registry.Machine) (string, error) {
	ctx, cancel := context.WithTimeout(d.a.ctx, 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	code, err := d.a.ssh.Shell(ctx, d.a.target(m), doctor.AuthorizedKeysScript, transport.IO{Stdout: &out})
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("exit %d", code)
	}
	return out.String(), nil
}

// agentsChecks: the agents store here. files that can't sync fail; conflicts,
// waiting reviews, lost deletes, broken links and home paths warn.
func agentsChecks(self string) []doctor.Check {
	const g = "agents store"
	r, err := agents.StoreReport(self)
	if err != nil {
		return []doctor.Check{{Group: g, Name: "state", Status: doctor.Fail, Detail: err.Error(), Fix: "fix or delete the file named above"}}
	}
	var out []doctor.Check
	add := func(name string, st doctor.Status, detail, fix string) {
		out = append(out, doctor.Check{Group: g, Name: name, Status: st, Detail: detail, Fix: fix})
	}
	detail := fmt.Sprintf("%d files in %s", r.Files, agents.Tilde(r.Dir, agents.Home()))
	if len(r.LastSync) > 0 {
		var parts []string
		for _, m := range sortedKeys(r.LastSync) {
			parts = append(parts, m+" "+ago(r.LastSync[m]))
		}
		detail += "; last sync: " + strings.Join(parts, ", ")
	}
	add("store", doctor.Pass, detail, "")
	for _, ref := range r.Refused {
		add("refused", doctor.Fail, ref.Path+": "+ref.Reason+" (it doesn't sync)", ref.Fix)
	}
	for _, c := range r.Conflicts {
		add("conflict", doctor.Warn, c+" was edited on two machines at once; both versions are in the store",
			"yonder sync --resolve "+c+" --keep mine|theirs|<machine>, or delete the .conflict- copy")
	}
	for _, p := range r.Pending {
		add("review", doctor.Warn, p+" changed on another machine and waits for review here", "yonder agents accept (in a terminal)")
	}
	for _, n := range r.Notices {
		add("delete", doctor.Warn, n.Path+": "+n.Detail, "delete it again if you meant to")
	}
	for _, p := range r.HomePaths {
		add("home path", doctor.Warn, p+" names an absolute home folder, which differs on other machines", "write ~/ instead")
	}
	for _, p := range r.Problems {
		add("link", doctor.Warn, p, "yonder agents link")
	}
	switch {
	case r.Paused:
		add("link", doctor.Warn, "links are paused although the layer is on", "yonder sync")
	case len(r.Linked) == 0:
		add("link", doctor.Pass, "not linked into any harness here", "")
	case len(r.Problems) == 0:
		add("link", doctor.Pass, "linked into "+strings.Join(r.Linked, ", "), "")
	}
	return out
}
