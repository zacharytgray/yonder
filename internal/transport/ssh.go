package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// SSH runs commands with the system ssh binary.
type SSH struct {
	Binary   string // default "ssh"
	Identity string // optional private key, mostly for tests
}

const (
	strict    = "yes"
	acceptNew = "accept-new"
)

func alias(t Target) string { return "yonder-" + t.Name }

// noMux keeps a ControlMaster from the user's ssh config out: a shared
// connection would skip the pinned host key check.
var noMux = []string{"-o", "ControlMaster=no", "-o", "ControlPath=none"}

// opts are the -o flags shared by ssh, scp and ssh-copy-id.
func (s *SSH) opts(t Target, knownHosts, checking string) []string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	o := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "Port=" + strconv.Itoa(port),
		"-o", "HostKeyAlias=" + alias(t),
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=" + checking,
		"-o", "HashKnownHosts=no",
		"-o", "UpdateHostKeys=no",
		"-o", "LogLevel=ERROR",
	}
	o = append(o, noMux...)
	if t.User != "" {
		o = append(o, "-o", "User="+t.User)
	}
	if id := s.identity(t); id != "" {
		o = append(o, "-i", id, "-o", "IdentitiesOnly=yes")
	}
	return o
}

// identity is the key for t: its own setting wins over the runner-wide one.
func (s *SSH) identity(t Target) string {
	if t.Identity != "" {
		return t.Identity
	}
	return s.Identity
}

// knownHosts writes a temp known_hosts holding the pinned key (or nothing).
func knownHosts(t Target, key string) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "yonder-kh-")
	if err != nil {
		return "", nil, err
	}
	path = filepath.Join(dir, "known_hosts")
	var data []byte
	if key != "" {
		data = []byte(alias(t) + " " + strings.TrimSpace(key) + "\n")
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { os.RemoveAll(dir) }, nil
}

func (s *SSH) bin() string {
	if s.Binary != "" {
		return s.Binary
	}
	return "ssh"
}

func (s *SSH) Yonder(ctx context.Context, t Target, args []string, stdio IO) (int, error) {
	path := t.YonderPath
	if path == "" {
		path = "yonder"
	}
	return s.run(ctx, t, Quote(path)+" "+Join(args), stdio)
}

func (s *SSH) Shell(ctx context.Context, t Target, script string, stdio IO) (int, error) {
	return s.run(ctx, t, "sh -c "+Quote(script), stdio)
}

func (s *SSH) run(ctx context.Context, t Target, remote string, stdio IO) (int, error) {
	if t.HostKey == "" {
		return 1, fmt.Errorf("%s has no pinned host key; run `yonder add %s`", t.Name, t.Name)
	}
	kh, cleanup, err := knownHosts(t, t.HostKey)
	if err != nil {
		return 1, err
	}
	defer cleanup()
	return s.exec(ctx, t, kh, strict, remote, stdio)
}

// Pin runs a sh script trusting whatever host key the target presents
// (accept-new against an empty known_hosts) and returns that key.
// the key comes back even when auth fails, since ssh checks it first.
func (s *SSH) Pin(ctx context.Context, t Target, script string, stdio IO) (key string, code int, err error) {
	kh, cleanup, err := knownHosts(t, "")
	if err != nil {
		return "", 1, err
	}
	defer cleanup()
	code, err = s.exec(ctx, t, kh, acceptNew, "sh -c "+Quote(script), stdio)
	if data, rerr := os.ReadFile(kh); rerr == nil {
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) >= 3 && f[0] == alias(t) {
				key = f[1] + " " + f[2]
				break
			}
		}
	}
	return key, code, err
}

func (s *SSH) exec(ctx context.Context, t Target, kh, checking, remote string, stdio IO) (int, error) {
	args := append([]string{"-T"}, s.opts(t, kh, checking)...)
	args = append(args, "--", t.Host, remote)
	cmd := exec.CommandContext(ctx, s.bin(), args...)
	tail := &tailWriter{w: stdio.Stderr}
	cmd.Stdout = orDiscard(stdio.Stdout)
	cmd.Stderr = tail
	err := startWithStdin(cmd, stdio.Stdin)
	if err == nil {
		err = cmd.Wait()
	}
	if err == nil {
		return 0, nil
	}
	code, ok := exitCode(err)
	if !ok {
		return 1, fmt.Errorf("ssh %s: %w", t.Name, err)
	}
	if code == 255 {
		return 255, &UnreachableError{Target: t.Name, Detail: tail.detail()}
	}
	return code, nil
}

// Copy sends a local file to the target with scp.
func (s *SSH) Copy(ctx context.Context, t Target, local, remote string) error {
	kh, cleanup, err := knownHosts(t, t.HostKey)
	if err != nil {
		return err
	}
	defer cleanup()
	host := t.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	args := append([]string{"-q"}, s.opts(t, kh, strict)...)
	args = append(args, "--", local, host+":"+remote)
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "scp", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scp to %s: %w: %s", t.Name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// CopyID runs ssh-copy-id attached to the terminal so the user can type a password.
func (s *SSH) CopyID(ctx context.Context, t Target, stdio IO) error {
	kh, cleanup, err := knownHosts(t, t.HostKey)
	if err != nil {
		return err
	}
	defer cleanup()
	port := t.Port
	if port == 0 {
		port = 22
	}
	args := []string{
		"-p", strconv.Itoa(port),
		"-o", "HostKeyAlias=" + alias(t),
		"-o", "UserKnownHostsFile=" + kh,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=yes",
	}
	args = append(args, noMux...)
	if id := s.identity(t); id != "" {
		args = append(args, "-i", id, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, t.User+"@"+t.Host)
	cmd := exec.CommandContext(ctx, "ssh-copy-id", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdio.Stdin, stdio.Stdout, stdio.Stderr
	return cmd.Run()
}

// startWithStdin feeds stdin through a pipe without making Wait block on it,
// so a terminal stdin that never hits EOF can't hang us after the child exits.
func startWithStdin(cmd *exec.Cmd, stdin io.Reader) error {
	if stdin == nil {
		return cmd.Start()
	}
	w, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_, _ = io.Copy(w, stdin)
		_ = w.Close()
	}()
	return nil
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// tailWriter passes stderr through and keeps the last few KB for error messages.
type tailWriter struct {
	mu  sync.Mutex
	w   io.Writer
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	t.mu.Unlock()
	if t.w != nil {
		return t.w.Write(p)
	}
	return len(p), nil
}

func (t *tailWriter) detail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		for _, k := range []string{"ssh:", "Permission denied", "Host key verification failed", "Connection", "REMOTE HOST IDENTIFICATION"} {
			if strings.Contains(l, k) {
				return l
			}
		}
	}
	if n := len(lines); n > 0 {
		return strings.TrimSpace(lines[n-1])
	}
	return ""
}

var _ Runner = (*SSH)(nil)

// IsUnreachable is errors.Is(err, ErrUnreachable) with the concrete error.
func IsUnreachable(err error) (*UnreachableError, bool) {
	var ue *UnreachableError
	if errors.As(err, &ue) {
		return ue, true
	}
	return nil, false
}
