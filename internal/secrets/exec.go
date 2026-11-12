package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/zacharytgray/yonder/internal/discover"
)

// test hooks: tests swap these for fakes so nothing real runs.
var (
	run      = execRun
	lookPath = findTool
)

// findTool looks past a bare PATH (ssh, launchd) into the usual install dirs.
func findTool(name string) (string, error) {
	if p := discover.Find(name); p != "" {
		return p, nil
	}
	return "", exec.ErrNotFound
}

// execRun runs bin with env replacing the environment. stdin carries anything
// sensitive; argv never does.
func execRun(ctx context.Context, bin string, args, env []string, stdin []byte) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// resolveTimeout covers a person approving with touch id.
const (
	resolveTimeout = 3 * time.Minute
	checkTimeout   = 15 * time.Second
)

// childEnv is this process's env minus the provider token vars, plus extra.
func childEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "OP_SERVICE_ACCOUNT_TOKEN" || k == "BWS_ACCESS_TOKEN" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// providerVars are credentials yonder or a provider cli reads from the
// environment. a command run for an agent never inherits them; a --secret
// of the same name is added back by the caller.
var providerVars = []string{"OP_SERVICE_ACCOUNT_TOKEN", "BWS_ACCESS_TOKEN", "YONDER_OP_TOKEN", "OP_CONNECT_TOKEN", "OP_CONNECT_HOST"}

// IsProviderVar reports whether name is a provider credential, OP_SESSION_* included.
func IsProviderVar(name string) bool {
	return slices.Contains(providerVars, name) || strings.HasPrefix(name, "OP_SESSION_")
}

// WithoutProviderVars is env minus every provider credential.
func WithoutProviderVars(env []string) []string {
	var out []string
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !IsProviderVar(k) {
			out = append(out, kv)
		}
	}
	return out
}

// providerCreds maps each provider credential set in env to its value, for
// scrubbing a child's stderr.
func providerCreds(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); IsProviderVar(k) && v != "" {
			out[k] = v
		}
	}
	return out
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// lastLine is the last non-empty stderr line, minus op's "[ERROR] date time" prefix.
func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if strings.HasPrefix(l, "[ERROR] ") {
		if f := strings.SplitN(l, " ", 4); len(f) == 4 {
			l = f[3]
		}
	}
	return l
}
