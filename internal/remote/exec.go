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
	cmd := shellCommand(ctx, env.Command)
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
	PID       int        `json:"pid"`
	State     string     `json:"state"` // running | exited | lost
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
	_ = writeStatus(dir, st)
	w := &lockedWriter{w: logf}
	code, runErr := RunCommand(ctx, env, nil, w, w)
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
