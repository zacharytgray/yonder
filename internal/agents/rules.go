package agents

import (
	"bytes"
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/store"
)

// StoreName is the agents store's name and folder.
const StoreName = "agents"

func init() {
	store.Register(store.Kind{
		Name:        StoreName,
		Layer:       registry.LayerAgents,
		Ignore:      ignore,
		Check:       Check,
		Review:      review,
		AfterChange: afterChange,
	})
}

func ignore(rel string, dir bool) bool {
	base := path.Base(rel)
	return (dir && base == ".git") || base == ".DS_Store"
}

// review settings: [sync] review in config.toml
const (
	ReviewExecutables = "executables"
	ReviewAll         = "all"
	ReviewNone        = "none"
)

// ReviewMode is this machine's [sync] review, defaulting to executables.
func ReviewMode() string {
	c, err := registry.Open().Config()
	if err != nil {
		return ReviewExecutables
	}
	switch c.Sync.Review {
	case ReviewAll, ReviewNone:
		return c.Sync.Review
	}
	return ReviewExecutables
}

// layout is rel's parts below any machines/<name>/, folded to lower case:
// a Mac keeps Hooks/ and hooks/ in one folder, so every rule matches both.
func layout(rel string) []string {
	parts := strings.Split(strings.ToLower(rel), "/")
	if len(parts) >= 3 && parts[0] == "machines" {
		parts = parts[2:]
	}
	return parts
}

// Runs: hooks, executable files and everything linked into a harness's own
// folder (hook configs, MCP configs, catalogs) can run code or change what
// runs, so an incoming change to one waits for review by default.
func Runs(rel string, exec bool) bool {
	if exec {
		return true
	}
	parts := layout(rel)
	return len(parts) >= 2 && (parts[0] == "hooks" || parts[0] == "files")
}

// a harness's own settings, auth and instruction files: never under files/,
// since linking one would replace the user's file
var harnessFiles = []string{
	"settings*.json", "config.toml", "config.yaml", "config.yml", ".claude.json",
	"auth.json", ".credentials*", "credentials*",
	"claude.md", "claude.local.md", "agents.md", "soul.md",
}

// HarnessFile reports whether a files/<key>/ name is one of those.
func HarnessFile(name string) bool {
	name = strings.ToLower(name)
	for _, pat := range harnessFiles {
		if ok, _ := path.Match(pat, name); ok {
			return true
		}
	}
	return false
}

func review(rel string, exec bool) bool {
	switch ReviewMode() {
	case ReviewAll:
		return true
	case ReviewNone:
		return false
	}
	return Runs(rel, exec)
}

// names that never sync, matched on the file name
var secretNames = []string{".env*", "*.pem", "*.key", "*.p12", "*.pfx", "id_*", "auth.json", "credentials*", ".credentials*",
	".git-credentials", ".netrc", "_netrc", "*.age", "*token*", "*secret*"}

// the strings that open yonder's own blocks and rendered files
const markerPrefix = "<!-- yonder:"

// skill folders that belong to one machine or another tool
var reservedSkills = map[string]string{
	"yonder": "each machine installs its own yonder skill with `yonder agents install`",
	"synced": "synced/ is Claude.ai's own skills folder",
}

// Check refuses secret-looking names and contents, and reserved skill folders.
func Check(rel string, data []byte) *store.Refusal {
	base := strings.ToLower(path.Base(rel))
	for _, pat := range secretNames {
		if ok, _ := path.Match(pat, base); ok {
			return &store.Refusal{Reason: fmt.Sprintf("the name %s looks like a secret (%s)", path.Base(rel), pat),
				Fix: "secrets never sync; keep it out of the store (references like op://... are fine inside a file)", Final: true}
		}
	}
	parts := layout(rel)
	if len(parts) >= 2 && parts[0] == "skills" {
		if why, ok := reservedSkills[parts[1]]; ok {
			return &store.Refusal{Reason: "the name skills/" + parts[1] + " is reserved: " + why, Fix: "remove it from the store", Final: true}
		}
	}
	if len(parts) >= 3 && parts[0] == "files" && HarnessFile(parts[len(parts)-1]) {
		return &store.Refusal{Reason: "the name " + rel + " is a harness's own settings, auth or instructions file, which yonder never syncs",
			Fix: "keep it out of the store; put instructions in instructions.md", Final: true}
	}
	if isInstructions(rel) && bytes.Contains(data, []byte(markerPrefix)) {
		return &store.Refusal{Reason: "it holds yonder's own marker (" + markerPrefix + "...), which would break the block it is rendered into",
			Fix: "take that text out", Final: true}
	}
	if line, what := secretContent(data); line > 0 {
		return &store.Refusal{Reason: fmt.Sprintf("looks like %s (line %d)", what, line),
			Fix: "take the value out and use a secret name or an op:// reference, or `yonder agents allow " + rel + "` if it is not a secret"}
	}
	return nil
}

var secretPatterns = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`), "a private key"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "an API key (sk-...)"},
	{regexp.MustCompile(`\b(ghp|gho|ghs|ghu|ghr)_[A-Za-z0-9]{30,}`), "a GitHub token"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{30,}`), "a GitHub token"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "a Slack token"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), "an AWS access key"},
}

var tokenRun = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)

// secretContent finds the first line that looks like it holds a key. it
// catches common formats, not everything.
func secretContent(data []byte) (line int, what string) {
	binary := bytes.IndexByte(data, 0) >= 0
	for i, l := range bytes.Split(data, []byte("\n")) {
		for _, p := range secretPatterns {
			if p.re.Match(l) {
				return i + 1, p.what
			}
		}
		if binary || bytes.Contains(l, []byte("op://")) || bytes.Contains(l, []byte("bws://")) {
			continue
		}
		for _, run := range tokenRun.FindAll(l, -1) {
			if highEntropy(run) {
				return i + 1, "a long random token"
			}
		}
	}
	return 0, ""
}

// highEntropy: mixed case and digits, and close to random. hex hashes and
// long words don't count.
func highEntropy(s []byte) bool {
	var upper, lower, digit bool
	counts := map[byte]int{}
	for _, c := range s {
		counts[c]++
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= '0' && c <= '9':
			digit = true
		}
	}
	if !upper || !lower || !digit {
		return false
	}
	h := 0.0
	n := float64(len(s))
	for _, k := range counts {
		p := float64(k) / n
		h -= p * math.Log2(p)
	}
	return h >= 4.5
}

var homePath = regexp.MustCompile(`(/Users|/home)/[A-Za-z0-9._-]+/`)

// HomePaths finds absolute home paths in an instructions file, which point
// at one machine's home and break on the others. "~/" works everywhere.
func HomePaths(data []byte) []int {
	var lines []int
	for i, l := range bytes.Split(data, []byte("\n")) {
		if homePath.Match(l) {
			lines = append(lines, i+1)
		}
	}
	return lines
}
