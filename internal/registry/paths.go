package registry

import (
	"os"
	"path/filepath"
	"strings"
)

// ConfigDir is where config.toml, machines/ and removed/ live.
// YONDER_HOME wins over XDG_CONFIG_HOME, which wins over ~/.config.
func ConfigDir() string {
	if h := yonderHome(); h != "" {
		return filepath.Join(h, "config")
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "yonder")
	}
	return filepath.Join(homeDir(), ".config", "yonder")
}

// StateDir is where log.jsonl and jobs/ live.
func StateDir() string {
	if h := yonderHome(); h != "" {
		return filepath.Join(h, "state")
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "yonder")
	}
	return filepath.Join(homeDir(), ".local", "state", "yonder")
}

// DataDir holds the synced stores (agents/).
func DataDir() string {
	if h := yonderHome(); h != "" {
		return filepath.Join(h, "data")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "yonder")
	}
	return filepath.Join(homeDir(), ".local", "share", "yonder")
}

func LogPath() string { return filepath.Join(StateDir(), "log.jsonl") }
func JobsDir() string { return filepath.Join(StateDir(), "jobs") }

// UserHome is ~ for files yonder touches outside its own dirs: harness skill
// folders and ~/.ssh/authorized_keys. under YONDER_HOME it is $YONDER_HOME/home,
// so tests never reach the real home.
func UserHome() string {
	if h := yonderHome(); h != "" {
		return filepath.Join(h, "home")
	}
	return homeDir()
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// yonderHome is YONDER_HOME made absolute, so detached children agree on it.
func yonderHome() string {
	h := os.Getenv("YONDER_HOME")
	if h == "" {
		return ""
	}
	if abs, err := filepath.Abs(h); err == nil {
		return abs
	}
	return h
}

// ExpandHome turns a leading ~ into UserHome. other paths pass through.
func ExpandHome(p string) string {
	switch {
	case p == "~":
		return UserHome()
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(UserHome(), p[2:])
	}
	return p
}
