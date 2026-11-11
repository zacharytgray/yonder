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
		Secrets:      secrets.Health(),
		Remote:       doctorRemote{a},
		Now:          time.Now(),
	}
	checks := doctor.Run(e)
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
	code, err := d.a.ssh.Yonder(ctx, target(m), args, transport.IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb})
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
	code, err := d.a.ssh.Yonder(ctx, target(m), []string{"_sync"}, transport.IO{
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
	code, err := d.a.ssh.Shell(ctx, target(m), doctor.AuthorizedKeysScript, transport.IO{Stdout: &out})
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("exit %d", code)
	}
	return out.String(), nil
}
