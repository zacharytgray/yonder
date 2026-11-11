package cli

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/secrets"
	"github.com/zacharytgray/yonder/internal/transport"
)

func newRunCmd() *cobra.Command {
	var (
		secretNames []string
		detach      bool
		as          string
	)
	cmd := &cobra.Command{
		Use:   "run <machine> [--secret NAME]... [--detach] [--as harness] -- <command...>",
		Short: "Run a command on a fleet machine",
		Long: `Run a command on a fleet machine and stream its output.

One argument after -- is run as a shell string; several are quoted and joined.
The exit code is the command's own, or 75 if the machine is offline or unreachable.`,
		Args: usageArgs(func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("need a machine and a command: yonder run <machine> -- <command...>")
			}
			if d := cmd.ArgsLenAtDash(); d != -1 && d != 1 {
				return fmt.Errorf("put exactly one machine name before --")
			}
			return nil
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			harness := as
			if harness == "" {
				harness = detectHarness()
			}
			return runCommand(newApp(cmd), args[0], args[1:], secretNames, detach, harness)
		},
	}
	cmd.Flags().StringArrayVar(&secretNames, "secret", nil, "secret `NAME` to resolve here and pass as an env var (repeatable)")
	cmd.Flags().BoolVar(&detach, "detach", false, "start in the background on the target and print a job id")
	cmd.Flags().StringVar(&as, "as", "", "harness name to record in the log (default: detected from env)")
	return cmd
}

func commandString(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return transport.Join(args)
}

func runCommand(a *app, name string, cmdArgs, secretNames []string, detach bool, harness string) error {
	self, err := a.selfName()
	if err != nil {
		return err
	}
	m, err := a.machine(name)
	if err != nil {
		return err
	}
	names := []string{}
	seen := map[string]bool{}
	for _, n := range secretNames {
		if err := secrets.ValidName(n); err != nil {
			return &usageError{err}
		}
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	values, err := secrets.Resolve(names)
	if err != nil {
		return err
	}
	env := remote.Envelope{
		ID:      remote.NewID(),
		Command: commandString(cmdArgs),
		From:    self,
		To:      m.Name,
		Harness: harness,
		Secrets: values,
		Detach:  detach,
	}
	line, err := env.Encode()
	if err != nil {
		return err
	}
	start := time.Now()
	r, err := a.reach(m, self)
	if err != nil {
		a.logCaller(env, start, ExitTempErr, err)
		return err
	}
	code, err := r.Yonder(a.ctx, target(m), []string{"_exec"}, transport.IO{
		Stdin:  io.MultiReader(bytes.NewReader(line), a.in),
		Stdout: a.out,
		Stderr: a.errw,
	})
	if ue, ok := transport.IsUnreachable(err); ok {
		a.logCaller(env, start, ExitTempErr, ue)
		return unreachableError(m, ue)
	}
	if err != nil {
		a.logCaller(env, start, ExitError, err)
		return err
	}
	if detach && code == 0 {
		fmt.Fprintf(a.errw, "yonder: started on %s; output: yonder jobs %s %s\n", m.Name, m.Name, env.ID)
	}
	return quietExit(code)
}
