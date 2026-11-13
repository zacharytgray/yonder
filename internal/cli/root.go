package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/transport"
	"github.com/zacharytgray/yonder/internal/version"
)

// Exit codes, per docs/design.md.
const (
	ExitError   = 1
	ExitUsage   = 2
	ExitTempErr = 75 // target offline or unreachable
)

// exitError carries an exit code. an empty msg exits quietly.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func withCode(code int, format string, a ...any) error {
	return &exitError{code: code, msg: fmt.Sprintf(format, a...)}
}

func quietExit(code int) error {
	if code == 0 {
		return nil
	}
	return &exitError{code: code}
}

type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

func usageArgs(fn cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := fn(cmd, args); err != nil {
			return &usageError{err}
		}
		return nil
	}
}

// ExitCode maps an error from a command onto yonder's exit codes.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	if errors.Is(err, transport.ErrUnreachable) {
		return ExitTempErr
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return ExitUsage
	}
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand", "flag needs an argument", "invalid argument"} {
		if strings.HasPrefix(msg, p) {
			return ExitUsage
		}
	}
	return ExitError
}

// Execute runs yonder with args and returns the process exit code.
func Execute(args []string) int {
	root := newRoot()
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	code := ExitCode(err)
	if err != nil && err.Error() != "" {
		fmt.Fprintf(os.Stderr, "yonder: %v\n", err)
		if code == ExitUsage {
			fmt.Fprintln(os.Stderr, "run `yonder --help` for usage")
		}
	}
	return code
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "yonder",
		Short:         "Connect your own machines so the agents on any of them can use all of them",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.CompletionOptions.HiddenDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &usageError{err} })
	root.AddCommand(
		newVersionCmd(),
		newInitCmd(),
		newAddCmd(),
		newSSHKeyCmd(),
		newSetCmd(),
		newLsCmd(),
		newShowCmd(),
		newRunCmd(),
		newCpCmd(),
		newJobsCmd(),
		newLogCmd(),
		newRefreshCmd(),
		newDoctorCmd(),
		newMeshCmd(),
		newRemoveCmd(),
		newSecretCmd(),
		newAgentsCmd(),
		newSyncCmd(),
		newLayersCmd(),
		newEnableCmd(),
		newDisableCmd(),
	)
	root.AddCommand(hiddenCmds()...)
	return root
}
