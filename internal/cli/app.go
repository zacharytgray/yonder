package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/runlog"
	"github.com/zacharytgray/yonder/internal/secrets"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

// app is the per-invocation state shared by commands.
type app struct {
	ctx   context.Context
	store *registry.Store
	ssh   sshRunner
	local transport.Local
	in    io.Reader
	out   io.Writer
	errw  io.Writer

	st     *tailnet.Status
	stErr  error
	stDone bool

	clearLost []string // names re-added here; syncFleet asks peers to drop their lost tombstones
}

// sshRunner is transport.SSH plus the add-time calls; tests swap in a fake.
type sshRunner interface {
	transport.Runner
	Pin(ctx context.Context, t transport.Target, script string, stdio transport.IO) (key string, code int, err error)
	CopyID(ctx context.Context, t transport.Target, stdio transport.IO) error
	Copy(ctx context.Context, t transport.Target, local, remote string) error
}

func newApp(cmd *cobra.Command) *app {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return &app{
		ctx:   ctx,
		store: registry.Open(),
		ssh:   &transport.SSH{Identity: os.Getenv("YONDER_SSH_IDENTITY")},
		local: transport.Local{Handler: remote.Dispatch},
		in:    cmd.InOrStdin(),
		out:   cmd.OutOrStdout(),
		errw:  cmd.ErrOrStderr(),
	}
}

func (a *app) warnf(format string, args ...any) {
	fmt.Fprintf(a.errw, "yonder: "+format+"\n", args...)
}

// status is tailscale status, fetched once per invocation.
func (a *app) status() (*tailnet.Status, error) {
	if !a.stDone {
		a.st, a.stErr = tailnet.Get(a.ctx)
		a.stDone = true
	}
	return a.st, a.stErr
}

func (a *app) selfName() (string, error) {
	c, err := a.store.Config()
	if err != nil {
		return "", err
	}
	if c.Self == "" {
		return "", errors.New("this machine is not set up yet; run `yonder init`")
	}
	return c.Self, nil
}

func (a *app) machine(name string) (registry.Machine, error) {
	m, err := a.store.Machine(name)
	if errors.Is(err, registry.ErrNotFound) {
		known := []string{}
		if ms, _ := a.store.Machines(); ms != nil {
			for _, k := range ms {
				known = append(known, k.Name)
			}
		}
		hint := "run `yonder ls`"
		if len(known) > 0 {
			hint = "known: " + strings.Join(known, ", ")
		}
		return m, fmt.Errorf("no machine named %s in the registry (%s)", name, hint)
	}
	return m, err
}

// target builds the ssh target for m, with this machine's key for it if one is set.
func (a *app) target(m registry.Machine) transport.Target {
	port := m.SSHPort
	if port == 0 {
		port = 22
	}
	return transport.Target{
		Name:       m.Name,
		Host:       m.TailscaleIP,
		Port:       port,
		User:       m.SSHUser,
		HostKey:    m.SSHHostKey,
		YonderPath: m.YonderPath,
		Identity:   registry.ExpandHome(a.store.Identity(m.Name)),
	}
}

// reach checks that m can be reached right now and returns its runner.
// self runs in-process; everything else goes over ssh.
func (a *app) reach(m registry.Machine, self string) (transport.Runner, error) {
	if m.Name == self {
		return a.local, nil
	}
	st, err := a.status()
	if err != nil {
		return nil, withCode(ExitTempErr, "cannot check %s: %v", m.Name, err)
	}
	p, r := st.Reach(m.Name, m.DNSName, m.TailscaleIP)
	if r == tailnet.NotInTailnet || r == tailnet.Offline {
		return nil, reachError(m, p, r)
	}
	if m.Pending {
		return nil, withCode(ExitTempErr, "%s is not set up yet (it was offline when added); run `yonder refresh %s`", m.Name, m.Name)
	}
	if r != tailnet.Reachable {
		return nil, reachError(m, p, r)
	}
	return a.ssh, nil
}

// pinned is the check every ssh call to a recorded peer goes through: in the
// tailnet, online, and at the address its record pinned.
func (a *app) pinned(m registry.Machine) (tailnet.Peer, error) {
	st, err := a.status()
	if err != nil {
		return tailnet.Peer{}, withCode(ExitTempErr, "cannot check %s: %v", m.Name, err)
	}
	p, r := st.Reach(m.Name, m.DNSName, m.TailscaleIP)
	return p, reachError(m, p, r)
}

func reachError(m registry.Machine, p tailnet.Peer, r tailnet.Reachability) error {
	switch r {
	case tailnet.NotInTailnet:
		return withCode(ExitTempErr, "%s is not in tailscale status (renamed or removed from the tailnet?)%s", m.Name, notesSuffix(m))
	case tailnet.Offline:
		return offlineError(m, p)
	case tailnet.AddressChanged:
		return addressChanged(m, p)
	}
	return nil
}

// addressChanged refuses a peer whose tailnet address isn't the one on record.
func addressChanged(m registry.Machine, p tailnet.Peer) error {
	return withCode(ExitError, "%s's address changed (record %s, tailnet %s); run `yonder doctor`", m.Name, m.TailscaleIP, p.IP())
}

func offlineError(m registry.Machine, p tailnet.Peer) error {
	return withCode(ExitTempErr, "%s is offline (last seen %s)%s", m.Name, ago(p.LastSeen), notesSuffix(m))
}

func unreachableError(m registry.Machine, ue *transport.UnreachableError) error {
	return withCode(ExitTempErr, "%s%s", ue.Error(), notesSuffix(m))
}

func notesSuffix(m registry.Machine) string {
	if m.Notes == "" {
		return ""
	}
	return " — notes: " + m.Notes
}

// logCaller records a failed attempt on this machine's log.
func (a *app) logCaller(env remote.Envelope, start time.Time, code int, err error) {
	e := runlog.Entry{
		Time:     start.UTC(),
		ID:       env.ID,
		From:     env.From,
		To:       env.To,
		Harness:  env.Harness,
		User:     localUser(),
		Command:  secrets.ScrubText(env.Command, env.Secrets),
		Secrets:  env.SecretNames(),
		Exit:     runlog.Exit(code),
		MS:       time.Since(start).Milliseconds(),
		Side:     runlog.SideCaller,
		Detached: env.Detach,
	}
	if err != nil {
		e.Error = err.Error()
	}
	if werr := runlog.Append(registry.LogPath(), e); werr != nil {
		a.warnf("could not write log: %v", werr)
	}
}

func localUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// ago humanizes a last-seen time.
func ago(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// a real tty, not /dev/null (which is also a char device)
func isTerminal(f *os.File) bool {
	return term.IsTerminal(f.Fd())
}

// detectHarness guesses which agent harness launched us from its env markers.
func detectHarness() string {
	switch {
	case os.Getenv("CLAUDECODE") == "1":
		return "claude-code"
	case os.Getenv("CODEX_THREAD_ID") != "" || os.Getenv("CODEX_SANDBOX") != "":
		return "codex"
	case os.Getenv("PI_CODING_AGENT") == "true":
		return "pi"
	case os.Getenv("HERMES_SESSION_ID") != "":
		return "hermes"
	case os.Getenv("AI_AGENT") != "":
		return tailnet.Kebab(os.Getenv("AI_AGENT"))
	}
	return "shell"
}
