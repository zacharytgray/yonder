// Package test holds end-to-end tests that drive the built yonder binary.
package test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testVersion = "0.0.0-e2e"

var yonderBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "yonder-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	yonderBin = filepath.Join(dir, "yonder")
	if err := build(yonderBin, runtime.GOOS, runtime.GOARCH); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func goTool() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return "go"
}

func build(out, goos, goarch string) error {
	cmd := exec.Command(goTool(), "build", "-o", out,
		"-ldflags", "-X github.com/zacharytgray/yonder/internal/version.Version="+testVersion,
		"../cmd/yonder")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w\n%s", err, b)
	}
	return nil
}

// fleet is one fake machine's environment: its own YONDER_HOME and HOME.
type fleet struct {
	t    *testing.T
	home string
	env  []string
}

func newFleet(t *testing.T, fixture string) *fleet {
	t.Helper()
	home := t.TempDir()
	abs, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatal(err)
	}
	f := &fleet{t: t, home: home}
	f.env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"YONDER_HOME=" + filepath.Join(home, "yonder"),
		"YONDER_TAILSCALE_JSON=" + abs,
		"SHELL=/bin/sh",
		"USER=me",
	}
	return f
}

type result struct {
	code   int
	stdout string
	stderr string
}

func (f *fleet) run(stdin string, args ...string) result {
	f.t.Helper()
	cmd := exec.Command(yonderBin, args...)
	cmd.Env = f.env
	cmd.Dir = f.home
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		f.t.Fatalf("yonder %v: %v", args, err)
	}
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func (f *fleet) must(args ...string) result {
	f.t.Helper()
	r := f.run("", args...)
	if r.code != 0 {
		f.t.Fatalf("yonder %v: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r
}
