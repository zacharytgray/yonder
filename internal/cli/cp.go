package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/transport"
)

func newCpCmd() *cobra.Command {
	var (
		force     bool
		recursive bool
		as        string
	)
	cmd := &cobra.Command{
		Use:   "cp [--force] <src>... <dest>",
		Short: "Copy files and folders between fleet machines",
		Long: `Copy files and folders to, from or between fleet machines, like scp.

Each side is a local path or <machine>:<path>. All sources are on one machine.
A remote path that starts with ~ or is relative is under that machine's home.
Folders copy whole, with or without -r, so a Keynote file or an app bundle just works.

If <dest> ends in / or is an existing folder, each source goes inside it (a missing
folder ending in / is created). Otherwise the one source is copied to that name.
An existing file or folder is never replaced without --force.

Copying between two other machines relays the data through this one.
The exit code is 75 if a machine is offline or unreachable.

Examples:
  yonder cp ~/Documents/Talk.key laptop:~/Desktop/
  yonder cp gpu-box:~/runs/out.csv .
  yonder cp gpu-box:/tmp/weights desk:~/models/`,
		Args: usageArgs(cobra.MinimumNArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			harness := as
			if harness == "" {
				harness = detectHarness()
			}
			return runCopy(newApp(cmd), args[:len(args)-1], args[len(args)-1], force, harness)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing file or folder of the same name")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "accepted for scp habit; folders always copy whole")
	cmd.Flags().StringVar(&as, "as", "", "harness name to record in the log (default: detected from env)")
	return cmd
}

var machinePrefix = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// splitSide reads "machine:path". a colon after a / is part of a local path.
func splitSide(arg string) (machine, p string) {
	i := strings.Index(arg, ":")
	if i <= 0 || !machinePrefix.MatchString(arg[:i]) {
		return "", arg
	}
	return arg[:i], arg[i+1:]
}

// localPath makes a local side absolute, keeping a trailing / (it means "into").
func localPath(p string) (string, error) {
	abs, err := filepath.Abs(registry.ExpandHome(p))
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(p, "/") && abs != "/" {
		abs += "/"
	}
	return abs, nil
}

func runCopy(a *app, srcArgs []string, dstArg string, force bool, harness string) error {
	self, err := a.selfName()
	if err != nil {
		return err
	}
	named := false
	var srcName string
	var srcPaths []string
	for i, arg := range srcArgs {
		m, p := splitSide(arg)
		named = named || m != ""
		if m == "" {
			m = self
			if p, err = localPath(p); err != nil {
				return err
			}
		}
		if p == "" {
			return &usageError{fmt.Errorf("name a path after %s:", m)}
		}
		if i > 0 && m != srcName {
			return &usageError{errors.New("all sources must be on one machine; run cp once per machine")}
		}
		srcName = m
		srcPaths = append(srcPaths, p)
	}
	dstName, dstPath := splitSide(dstArg)
	named = named || dstName != ""
	switch {
	case dstName == "":
		dstName = self
		if dstPath, err = localPath(dstPath); err != nil {
			return err
		}
	case dstPath == "":
		dstPath = "~/"
	}
	if !named {
		return &usageError{errors.New("name a machine on one side, like laptop:~/Desktop/")}
	}
	src, err := a.machine(srcName)
	if err != nil {
		return err
	}
	dst, err := a.machine(dstName)
	if err != nil {
		return err
	}

	sides := make([]string, len(srcPaths))
	for i, p := range srcPaths {
		sides[i] = src.Name + ":" + p
	}
	env := remote.Envelope{
		ID:      remote.NewID(),
		Command: "cp " + strings.Join(sides, " ") + " -> " + dst.Name + ":" + dstPath,
		From:    src.Name,
		To:      dst.Name,
		Harness: harness,
		Copy:    &remote.CopySpec{Sources: srcPaths, Dest: dstPath, Force: force},
	}
	line, err := env.Encode()
	if err != nil {
		return err
	}
	start := time.Now()
	unreached := func(m string, code int, err error) error {
		e := env
		e.To = m
		a.logCaller(e, start, code, err)
		return err
	}
	srcR, err := a.reach(src, self)
	if err != nil {
		return unreached(src.Name, ExitCode(err), err)
	}
	dstR, err := a.reach(dst, self)
	if err != nil {
		return unreached(dst.Name, ExitCode(err), err)
	}

	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	pr, pw := io.Pipe()
	type result struct {
		code int
		err  error
	}
	var sendErr bytes.Buffer
	sent := make(chan result, 1)
	go func() {
		code, err := srcR.Yonder(ctx, a.target(src), []string{"_send"}, transport.IO{
			Stdin: bytes.NewReader(line), Stdout: pw, Stderr: &sendErr,
		})
		_ = pw.CloseWithError(err)
		sent <- result{code, err}
	}()
	var recvOut, recvErr bytes.Buffer
	rcode, rerr := dstR.Yonder(ctx, a.target(dst), []string{"_recv"}, transport.IO{
		Stdin: io.MultiReader(bytes.NewReader(line), pr), Stdout: &recvOut, Stderr: &recvErr,
	})
	if rcode != 0 || rerr != nil {
		cancel() // the sender may be blocked on a pipe nobody reads now
	}
	_ = pr.CloseWithError(io.ErrClosedPipe)
	s := <-sent

	if ue, ok := transport.IsUnreachable(rerr); ok {
		return unreached(dst.Name, ExitTempErr, unreachableError(dst, ue))
	}
	if rerr != nil {
		return rerr
	}
	if rcode == 0 {
		// warnings only: skipped sockets, links that point outside
		_, _ = a.errw.Write(sendErr.Bytes())
		_, _ = a.errw.Write(recvErr.Bytes())
		var res remote.CopyResult
		if err := json.Unmarshal([]byte(lastLine(recvOut.String())), &res); err != nil {
			return fmt.Errorf("copied, but %s's reply was unreadable: %v", dst.Name, err)
		}
		fmt.Fprintln(a.out, copySummary(dst.Name, res))
		return nil
	}
	if tooOld(rcode, recvErr.String()) {
		return fmt.Errorf("yonder on %s is too old for cp; upgrade it there", dst.Name)
	}
	if rcode != remote.ExitCutOff {
		// the receiver refused (an existing file, an unsafe archive); it said why
		_, _ = a.errw.Write(recvErr.Bytes())
		return quietExit(ExitError)
	}
	// the stream ended early, so the cause is on the sending side
	if ue, ok := transport.IsUnreachable(s.err); ok {
		return unreached(src.Name, ExitTempErr, unreachableError(src, ue))
	}
	if tooOld(s.code, sendErr.String()) {
		return fmt.Errorf("yonder on %s is too old for cp; upgrade it there", src.Name)
	}
	_, _ = a.errw.Write(sendErr.Bytes())
	_, _ = a.errw.Write(recvErr.Bytes())
	return quietExit(ExitError)
}

func copySummary(machine string, r remote.CopyResult) string {
	files := plural(r.Files, "file")
	if len(r.Items) == 1 {
		it := r.Items[0]
		detail := humanBytes(r.Bytes)
		if it.Dir {
			detail = "folder, " + files + ", " + detail
		}
		return fmt.Sprintf("copied %s (%s) to %s:%s", filepath.Base(it.Path), detail, machine, it.Path)
	}
	where := ""
	if len(r.Items) > 0 {
		where = filepath.Dir(r.Items[0].Path)
	}
	return fmt.Sprintf("copied %s (%s, %s) to %s:%s", plural(len(r.Items), "item"), files, humanBytes(r.Bytes), machine, where)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// humanBytes uses 1024 steps, like the GB in facts.
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		v /= 1024
		if v < 1024 || u == "TB" {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return ""
}
