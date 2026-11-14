package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/zacharytgray/yonder/internal/registry"
)

// CodexHome is the folder Codex reads its config and AGENTS.md from: the
// one the last explicit `agents link` here found, so a sync whose environment
// differs (the timer, a peer's ssh, another shell) never moves Codex's files;
// before any link, $CODEX_HOME when set, else ~/.codex.
func CodexHome(home string) string {
	if saved := savedCodexHome(); saved != "" {
		return saved
	}
	return codexHomeFromEnv(home)
}

// savedCodexHome is the Codex folder recorded in links.json, or "".
func savedCodexHome() string {
	data, err := os.ReadFile(filepath.Join(registry.StateDir(), "agents", "links.json"))
	if err != nil {
		return ""
	}
	var st struct {
		CodexHome string `json:"codex_home"`
	}
	if json.Unmarshal(data, &st) != nil {
		return ""
	}
	return st.CodexHome
}

// codexHomeFromEnv is $CODEX_HOME, else ~/.codex. under YONDER_HOME a
// CODEX_HOME counts only inside it, so a test never reaches a real one.
func codexHomeFromEnv(home string) string {
	if c := os.Getenv("CODEX_HOME"); c != "" && realHome(home) {
		if abs, err := filepath.Abs(c); err == nil {
			c = abs
		}
		if y := os.Getenv("YONDER_HOME"); y == "" || strings.HasPrefix(c, registry.UserHome()+string(filepath.Separator)) {
			return c
		}
	}
	return filepath.Join(home, ".codex")
}

// realHome: home is this user's home (or the test home under YONDER_HOME).
// CODEX_HOME belongs to that home only, never to a fixture folder.
func realHome(home string) bool {
	if os.Getenv("YONDER_HOME") != "" {
		return home == registry.UserHome()
	}
	h, err := os.UserHomeDir()
	return err == nil && filepath.Clean(h) == filepath.Clean(home)
}

// InstructionsPath is where a harness file named under ~ really is: Codex's
// files follow CODEX_HOME.
func InstructionsPath(home, rel string) string {
	if rest, ok := strings.CutPrefix(rel, ".codex/"); ok {
		return filepath.Join(CodexHome(home), rest)
	}
	if rel == ".codex" {
		return CodexHome(home)
	}
	return filepath.Join(home, rel)
}

// CodexOverride is Codex's AGENTS.override.md when it has text. Codex reads
// it instead of AGENTS.md, so yonder's rendered file goes unread there.
func CodexOverride(home string) (string, bool) {
	p := filepath.Join(CodexHome(home), "AGENTS.override.md")
	data, err := os.ReadFile(p)
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return p, false
	}
	return p, true
}

// CodexOverrideNote is the warning for that case.
func CodexOverrideNote(home string) string {
	p, _ := CodexOverride(home)
	return "Codex reads " + Tilde(p, home) + " instead of AGENTS.md when it has text, so it doesn't see yonder's instructions or briefing. Yonder never writes into it: move what it holds into the store (or into AGENTS.md), then delete it, or keep it and accept that Codex skips yonder's text"
}
