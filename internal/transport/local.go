package transport

import (
	"context"
	"io"
	"os/exec"
)

// Handler runs a yonder subcommand in-process.
type Handler func(ctx context.Context, args []string, in io.Reader, out, errw io.Writer) int

// Local is the runner for this machine: no ssh.
type Local struct {
	Handler Handler
}

func (l Local) Yonder(ctx context.Context, _ Target, args []string, stdio IO) (int, error) {
	return l.Handler(ctx, args, stdio.Stdin, orDiscard(stdio.Stdout), orDiscard(stdio.Stderr)), nil
}

func (l Local) Shell(ctx context.Context, _ Target, script string, stdio IO) (int, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Stdout, cmd.Stderr = orDiscard(stdio.Stdout), orDiscard(stdio.Stderr)
	err := startWithStdin(cmd, stdio.Stdin)
	if err == nil {
		err = cmd.Wait()
	}
	if err == nil {
		return 0, nil
	}
	if code, ok := exitCode(err); ok {
		return code, nil
	}
	return 1, err
}

var _ Runner = Local{}
