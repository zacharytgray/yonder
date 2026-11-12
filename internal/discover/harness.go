package discover

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
)

// name is the record name; bin is the executable.
var harnessTable = []struct{ name, bin string }{
	{"claude-code", "claude"},
	{"codex", "codex"},
	{"pi", "pi"},
	{"hermes", "hermes"},
	{"opencode", "opencode"},
	{"gemini", "gemini"},
}

func probeHarnesses(ctx context.Context) []registry.Harness {
	found := make([]*registry.Harness, len(harnessTable))
	var wg sync.WaitGroup
	for i, h := range harnessTable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := find(h.bin)
			if path == "" {
				return
			}
			real, ok := resolveShim(ctx, h.bin, path)
			if !ok {
				return
			}
			hr := registry.Harness{Name: h.name, Path: path}
			if real != "" {
				if out, ok := run(ctx, 2*time.Second, real, "--version"); ok {
					hr.Version = parseVersion(out)
				}
			}
			found[i] = &hr
		}()
	}
	wg.Wait()
	var hs []registry.Harness
	for _, h := range found {
		if h != nil {
			hs = append(hs, *h)
		}
	}
	return hs
}

var (
	versionRe = regexp.MustCompile(`\d+\.\d+(\.\d+)?([-+][0-9A-Za-z.]+)?`)
	ansiRe    = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
)

// parseVersion pulls the version out of `<bin> --version` output, e.g.
// "2.1.4 (Claude Code)", "codex-cli 0.46.0" or a bare "1.2.3". it takes the
// first line that has a version, so a banner or update notice above it is skipped.
func parseVersion(out string) string {
	lines := strings.Split(ansiRe.ReplaceAllString(out, ""), "\n")
	for _, l := range lines {
		if m := versionRe.FindString(l); m != "" {
			return m
		}
	}
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			if len(l) > 40 {
				l = l[:40]
			}
			return l
		}
	}
	return ""
}

// findMise is a var so tests can stand in for a machine without mise.
var findMise = func() string { return find("mise") }

// resolveShim returns the binary to ask for a version. a mise shim installs a
// missing tool when run, so it is resolved with `mise which` instead, which
// never installs. ok is false when mise says the tool isn't active: the shim
// is left over and the harness isn't really there. real is "" when mise
// itself can't be found, so the shim is never run.
func resolveShim(ctx context.Context, bin, path string) (real string, ok bool) {
	dir := filepath.Dir(path)
	if filepath.Base(dir) != "shims" || !strings.Contains(filepath.Base(filepath.Dir(dir)), "mise") {
		return path, true
	}
	mise := findMise()
	if mise == "" {
		return "", true
	}
	out, ok := run(ctx, 2*time.Second, mise, "which", bin)
	if !ok {
		return "", false
	}
	p := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if !filepath.IsAbs(p) {
		return "", false
	}
	return p, true
}
