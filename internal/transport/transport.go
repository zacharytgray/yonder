package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Target is what a runner needs to reach one machine.
type Target struct {
	Name       string
	Host       string
	Port       int
	User       string
	HostKey    string // pinned sshd host key, "type base64"
	YonderPath string
}

type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner runs things on a target. SSH and Local implement it; tests fake it.
type Runner interface {
	// Yonder runs `yonder <args>` on the target.
	Yonder(ctx context.Context, t Target, args []string, stdio IO) (int, error)
	// Shell runs a posix sh script on the target.
	Shell(ctx context.Context, t Target, script string, stdio IO) (int, error)
}

var ErrUnreachable = errors.New("unreachable")

// UnreachableError is ssh exit 255: no connection, auth failure, or host key mismatch.
type UnreachableError struct {
	Target string
	Detail string
}

func (e *UnreachableError) Error() string {
	if e.Detail == "" {
		return e.Target + " is unreachable over ssh"
	}
	return fmt.Sprintf("%s is unreachable over ssh (%s)", e.Target, e.Detail)
}

func (e *UnreachableError) Is(target error) bool { return target == ErrUnreachable }

// AuthFailed reports whether ssh failed on key auth rather than the network.
func (e *UnreachableError) AuthFailed() bool {
	return strings.Contains(e.Detail, "Permission denied")
}

// Quote single-quotes s for a posix shell. a leading = is quoted too (zsh's =cmd).
func Quote(s string) string {
	if s != "" && !strings.HasPrefix(s, "=") && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:@%+,") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join quotes each arg and joins with spaces.
func Join(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}

// exitCode pulls the exit status from an exec error.
func exitCode(err error) (int, bool) {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return code, true
		}
		return 128 + signalOf(ee), true
	}
	return 0, false
}
