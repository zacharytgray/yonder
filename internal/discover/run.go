package discover

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// extraDirs are searched after PATH; ssh shells often have a thin PATH.
func extraDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin", "/usr/sbin", "/sbin", "/usr/bin", "/bin"}
	if home != "" {
		dirs = append([]string{
			filepath.Join(home, ".local/bin"),
			filepath.Join(home, ".npm-global/bin"),
			filepath.Join(home, ".bun/bin"),
			filepath.Join(home, "go/bin"),
			filepath.Join(home, ".lmstudio/bin"),
			filepath.Join(home, ".cargo/bin"),
		}, dirs...)
	}
	return dirs
}

// find returns the path of a tool, or "" when it is not installed.
func find(name string, more ...string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, d := range append(extraDirs(), more...) {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// run executes a tool with its own timeout and returns stdout. ok is false
// when it failed, timed out or printed nothing.
func run(ctx context.Context, timeout time.Duration, bin string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "CI=1")
	cmd.WaitDelay = 200 * time.Millisecond // don't wait on stray grandchildren
	if err := cmd.Run(); err != nil {
		return "", false
	}
	s := out.String()
	if strings.TrimSpace(s) == "" {
		s = errb.String() // some tools print --version to stderr
	}
	return s, strings.TrimSpace(s) != ""
}

// Find is find for other packages: PATH first, then the usual install dirs,
// since ssh and launchd shells start with a bare PATH.
func Find(name string) string { return find(name) }
