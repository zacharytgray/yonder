package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/runlog"
	"github.com/zacharytgray/yonder/internal/secrets"
)

// Exec is `yonder _exec`: envelope line on stdin, then the command's stdin.
func Exec(ctx context.Context, _ []string, in io.Reader, out, errw io.Writer) int {
	br := bufio.NewReaderSize(in, 64<<10)
	env, err := ReadEnvelope(br)
	if err != nil {
		fmt.Fprintf(errw, "yonder _exec: %v\n", err)
		return 1
	}
	if env.Copy != nil {
		fmt.Fprintln(errw, "yonder _exec: this envelope is for cp, not a command")
		return 1
	}
	fillDefaults(&env)
	if env.Detach {
		return startDetached(env, out, errw)
	}
	start := time.Now()
	// a start entry first, so a run killed midway still shows up
	s := entry(env, start)
	s.Status = runlog.StatusStarted
	if err := runlog.Append(registry.LogPath(), s); err != nil {
		fmt.Fprintf(errw, "yonder: could not write log: %v\n", err)
	}
	code, runErr := RunCommand(ctx, env, br, out, errw)
	e := entry(env, start)
	e.Exit = runlog.Exit(code)
	if runErr != nil {
		e.Error = runErr.Error()
		fmt.Fprintf(errw, "yonder: %v\n", runErr)
	}
	if err := runlog.Append(registry.LogPath(), e); err != nil {
		fmt.Fprintf(errw, "yonder: could not write log: %v\n", err)
	}
	return code
}

func fillDefaults(env *Envelope) {
	if !ValidID(env.ID) {
		env.ID = NewID()
	}
	if env.To == "" {
		env.To = selfName()
	}
	if env.Harness == "" {
		env.Harness = "shell"
	}
}

func entry(env Envelope, start time.Time) runlog.Entry {
	return runlog.Entry{
		Time:    start.UTC(),
		ID:      env.ID,
		From:    env.From,
		To:      env.To,
		Harness: env.Harness,
		User:    username(),
		Command: secrets.ScrubText(env.Command, env.Secrets),
		Secrets: env.SecretNames(),
		MS:      time.Since(start).Milliseconds(),
		Side:    runlog.SideTarget,
	}
}

// RunCommand runs env.Command with secrets in its environment and scrubbed output.
func RunCommand(ctx context.Context, env Envelope, stdin io.Reader, out, errw io.Writer) (int, error) {
	return runCommand(ctx, env, stdin, out, errw, nil)
}

// runCommand is RunCommand. with started set, the command gets its own process
// group (so a job can be killed whole) and started gets its pid once it runs.
func runCommand(ctx context.Context, env Envelope, stdin io.Reader, out, errw io.Writer, started func(pid int)) (int, error) {
	cmd := shellCommand(ctx, env.Command)
	if started != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	// provider tokens stay out unless asked for by name with --secret
	cmd.Env = secrets.WithoutProviderVars(os.Environ())
	for _, name := range env.SecretNames() {
		cmd.Env = append(cmd.Env, name+"="+env.Secrets[name])
	}
	so := secrets.NewScrubber(out, env.Secrets)
	se := secrets.NewScrubber(errw, env.Secrets)
	cmd.Stdout, cmd.Stderr = so, se

	var err error
	if stdin != nil {
		var w io.WriteCloser
		if w, err = cmd.StdinPipe(); err == nil {
			if err = cmd.Start(); err == nil {
				// don't wait on this: a terminal stdin may never close
				go func() {
					_, _ = io.Copy(w, stdin)
					_ = w.Close()
				}()
			}
		}
	} else {
		err = cmd.Start()
	}
	if err != nil {
		return 127, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	if started != nil {
		started(cmd.Process.Pid)
	}
	err = cmd.Wait()
	_ = so.Flush()
	_ = se.Flush()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return ee.ExitCode(), nil
	}
	return 1, err
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	sh := os.Getenv("SHELL")
	if sh == "" || !executable(sh) {
		sh = "/bin/sh"
	}
	return exec.CommandContext(ctx, sh, "-lc", command)
}

func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

func username() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

func selfName() string {
	if c, err := registry.Open().Config(); err == nil && c.Self != "" {
		return c.Self
	}
	h, _ := os.Hostname()
	return h
}

// JobStatus is jobs/<id>.json, next to jobs/<id>.log.
type JobStatus struct {
	ID        string     `json:"id"`
	Command   string     `json:"command"`
	From      string     `json:"from"`
	Harness   string     `json:"harness"`
	Secrets   []string   `json:"secrets"`
	PID       int        `json:"pid"`            // the _job supervisor
	PGID      int        `json:"pgid,omitempty"` // the command's process group; 0 before yonder recorded it
	State     string     `json:"state"`          // running | exited | lost
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	Exit      *int       `json:"exit"`
}

// startDetached re-runs this binary as `_job` in a new session and returns at once.
func startDetached(env Envelope, out, errw io.Writer) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(errw, "yonder: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(registry.JobsDir(), 0o700); err != nil {
		fmt.Fprintf(errw, "yonder: %v\n", err)
		return 1
	}
	start := time.Now()
	cmd := exec.Command(exe, "_job")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	w, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		fmt.Fprintf(errw, "yonder: start job: %v\n", err)
		return 1
	}
	data, _ := json.Marshal(env)
	_, werr := w.Write(data)
	_ = w.Close()
	_ = cmd.Process.Release()
	if werr != nil {
		fmt.Fprintf(errw, "yonder: start job: %v\n", werr)
		return 1
	}
	e := entry(env, start)
	e.Detached = true
	if err := runlog.Append(registry.LogPath(), e); err != nil {
		fmt.Fprintf(errw, "yonder: could not write log: %v\n", err)
	}
	fmt.Fprintln(out, env.ID)
	return 0
}

// Job is `yonder _job`, the detached supervisor. it reads the envelope from
// stdin, runs the command with output scrubbed into jobs/<id>.log, and records
// the exit code in jobs/<id>.json.
func Job(ctx context.Context, _ []string, in io.Reader, _, errw io.Writer) int {
	signal.Ignore(syscall.SIGHUP)
	data, err := io.ReadAll(io.LimitReader(in, maxEnvelope))
	if err != nil {
		return 1
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil || !ValidID(env.ID) {
		fmt.Fprintf(errw, "yonder _job: bad envelope\n")
		return 1
	}
	dir := registry.JobsDir()
	logf, err := os.OpenFile(filepath.Join(dir, env.ID+".log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 1
	}
	defer logf.Close()
	st := JobStatus{
		ID: env.ID, Command: secrets.ScrubText(env.Command, env.Secrets), From: env.From, Harness: env.Harness,
		Secrets: env.SecretNames(), PID: os.Getpid(), State: "running", StartedAt: time.Now().UTC(),
	}
	w := &lockedWriter{w: logf}
	// "running" is written once the command has its group, so a running job always has a pgid
	code, runErr := runCommand(ctx, env, nil, w, w, func(pid int) {
		st.PGID = pid
		_ = writeStatus(dir, st)
	})
	if runErr != nil {
		fmt.Fprintf(w, "yonder: %v\n", runErr)
	}
	end := time.Now().UTC()
	st.State, st.EndedAt, st.Exit = "exited", &end, runlog.Exit(code)
	_ = writeStatus(dir, st)
	return code
}

func writeStatus(dir string, st JobStatus) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+st.ID+".json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, st.ID+".json"))
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// how long --kill waits after SIGTERM before SIGKILL
var killGrace = 10 * time.Second

// Kill is `yonder _kill <id>`: envelope line on stdin (for the log), then it
// stops a running job: SIGTERM to the command's process group, SIGKILL if
// anything in it is still there after killGrace. the kill is logged here.
func Kill(ctx context.Context, args []string, in io.Reader, out, errw io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(errw, "usage: yonder _kill <id>")
		return 2
	}
	id := args[0]
	if !ValidID(id) {
		fmt.Fprintf(errw, "yonder: bad job id %q\n", id)
		return 2
	}
	env, err := ReadEnvelope(bufio.NewReader(in))
	if err != nil {
		fmt.Fprintf(errw, "yonder _kill: %v\n", err)
		return 1
	}
	fillDefaults(&env)
	start := time.Now()
	msg, command, err := killJob(ctx, registry.JobsDir(), id)
	e := entry(env, start)
	e.Command = "kill job " + id
	if command != "" {
		e.Command += ": " + command
	}
	code := 0
	if err != nil {
		code = 1
		e.Error = err.Error()
		fmt.Fprintf(errw, "yonder: %v\n", err)
	} else {
		fmt.Fprintln(out, msg)
	}
	e.Exit = runlog.Exit(code)
	if err := runlog.Append(registry.LogPath(), e); err != nil {
		fmt.Fprintf(errw, "yonder: could not write log: %v\n", err)
	}
	return code
}

// killJob stops job id and says how. command is the job's (scrubbed) command.
func killJob(ctx context.Context, dir, id string) (msg, command string, err error) {
	st, err := waitStatus(ctx, dir, id)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("no job %s", id)
	}
	if err != nil {
		return "", "", err
	}
	command = st.Command
	if st.State != "running" {
		how := st.State
		if st.Exit != nil {
			how = fmt.Sprintf("exited %d", *st.Exit)
		}
		return "", command, fmt.Errorf("job %s is not running (%s)", id, how)
	}
	// jobs from before pgid was recorded: the supervisor leads its own session
	pg := st.PGID
	if pg == 0 {
		pg = st.PID
	}
	if pg <= 1 {
		return "", command, fmt.Errorf("job %s has no process group on record", id)
	}
	if err := syscall.Kill(-pg, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return "", command, fmt.Errorf("stop job %s: %w", id, err)
	}
	sig := syscall.SIGTERM
	deadline := time.Now().Add(killGrace)
	for groupAlive(pg) && time.Now().Before(deadline) {
		if !sleepCtx(ctx, jobPoll) {
			return "", command, ctx.Err()
		}
	}
	if groupAlive(pg) {
		sig = syscall.SIGKILL
		if err := syscall.Kill(-pg, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return "", command, fmt.Errorf("kill job %s: %w", id, err)
		}
	}
	// the supervisor records the exit; give it a moment
	for wait := time.Now().Add(3 * time.Second); time.Now().Before(wait); {
		if cur, err := loadStatus(dir, id); err == nil && cur.State != "running" {
			st = cur
			break
		}
		if !sleepCtx(ctx, jobPoll) {
			break
		}
	}
	if st.State == "lost" {
		// the supervisor died with the group (an older job): record the end ourselves
		end := time.Now().UTC()
		st.State, st.EndedAt, st.Exit = "exited", &end, runlog.Exit(128+int(sig))
		_ = writeStatus(dir, st)
	}
	name := "SIGTERM"
	if sig == syscall.SIGKILL {
		name = fmt.Sprintf("SIGKILL after %s", killGrace)
	}
	exit := "?"
	if st.Exit != nil {
		exit = fmt.Sprint(*st.Exit)
	}
	return fmt.Sprintf("stopped job %s with %s (exit %s)", id, name, exit), command, nil
}

// waitStatus is loadStatus, but waits a few seconds for a job whose log is
// there and whose status isn't yet (it is still starting).
func waitStatus(ctx context.Context, dir, id string) (JobStatus, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := loadStatus(dir, id)
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			return st, err
		}
		if _, lerr := os.Stat(filepath.Join(dir, id+".log")); lerr != nil {
			return st, err
		}
		if !sleepCtx(ctx, jobPoll) {
			return st, err
		}
	}
}

// groupAlive says whether any process is left in group pg.
func groupAlive(pg int) bool {
	err := syscall.Kill(-pg, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
