// Package agents installs the embedded yonder skill into each agent harness's
// user skills folder. it writes only its own yonder/ folder and never touches
// a harness's settings.
package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/version"
	"github.com/zacharytgray/yonder/skill"
)

// Harness is one agent harness and where it reads user skills from.
type Harness struct {
	Name    string
	Bins    []string // binaries that mean it's installed
	Config  string   // its config dir under ~, which also means it's installed
	Skills  string   // its user skills dir under ~
	Aliases []string
}

// Harnesses, researched 2026-11. claude code doesn't read ~/.agents/skills.
var Harnesses = []Harness{
	{Name: "claude-code", Bins: []string{"claude"}, Config: ".claude", Skills: ".claude/skills", Aliases: []string{"claude"}},
	{Name: "codex", Bins: []string{"codex"}, Config: ".codex", Skills: ".agents/skills"},
	{Name: "pi", Bins: []string{"pi"}, Config: ".pi", Skills: ".agents/skills"},
	{Name: "hermes", Bins: []string{"hermes"}, Config: ".hermes", Skills: ".hermes/skills"},
}

const (
	folder      = "yonder"
	versionFile = ".yonder-version"
)

// Home is the ~ the skill folders live under.
func Home() string { return registry.UserHome() }

// Lookup finds a harness by name or alias.
func Lookup(name string) (Harness, bool) {
	for _, h := range Harnesses {
		if h.Name == name {
			return h, true
		}
		for _, a := range h.Aliases {
			if a == name {
				return h, true
			}
		}
	}
	return Harness{}, false
}

// Names lists the harness names.
func Names() []string {
	out := make([]string, len(Harnesses))
	for i, h := range Harnesses {
		out[i] = h.Name
	}
	return out
}

// Path is where the skill folder goes for h.
func (h Harness) Path(home string) string {
	return filepath.Join(home, h.Skills, folder)
}

// Detect reports whether h is installed: a binary on PATH or its config dir.
func (h Harness) Detect(home string) (bool, string) {
	for _, b := range h.Bins {
		if p, err := exec.LookPath(b); err == nil {
			return true, p
		}
	}
	if st, err := os.Stat(filepath.Join(home, h.Config)); err == nil && st.IsDir() {
		return true, "~/" + h.Config
	}
	return false, ""
}

// Hash fingerprints the embedded skill files.
func Hash() string {
	sum := sha256.New()
	_ = fs.WalkDir(skill.FS, folder, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := skill.FS.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(sum, "%s\x00%d\x00", p, len(data))
		sum.Write(data)
		return nil
	})
	return hex.EncodeToString(sum.Sum(nil))[:12]
}

// stamp is what .yonder-version holds: "<version> <hash>".
func stamp() string { return version.Version + " " + Hash() + "\n" }

func readStamp(dir string) (ver, hash string, ok bool) {
	data, err := os.ReadFile(filepath.Join(dir, versionFile))
	if err != nil {
		return "", "", false
	}
	f := strings.Fields(string(data))
	if len(f) < 2 {
		return "", "", false
	}
	return f[0], f[1], true
}

// States of an installed skill folder.
const (
	StateCurrent  = "current"
	StateOutdated = "outdated"
	StateMissing  = "missing"
	StateForeign  = "foreign" // a symlink or a file; yonder leaves it alone
)

// HarnessStatus is one harness in `agents status --json`.
type HarnessStatus struct {
	Name      string `json:"name"`
	Detected  bool   `json:"detected"`
	Path      string `json:"path"`
	State     string `json:"state"`
	Installed string `json:"installed_version"` // from .yonder-version; "" if missing or unknown
}

// Report is `agents status --json`.
type Report struct {
	Version   string          `json:"version"`
	SkillHash string          `json:"skill_hash"`
	Harnesses []HarnessStatus `json:"harnesses"`
}

func stateOf(dir string) (state, installed string) {
	st, err := os.Lstat(dir)
	if err != nil {
		return StateMissing, ""
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return StateForeign, ""
	}
	ver, hash, ok := readStamp(dir)
	if !ok {
		return StateOutdated, ""
	}
	if hash != Hash() {
		return StateOutdated, ver
	}
	return StateCurrent, ver
}

// Status reports every harness's skill folder under home.
func Status(home string) Report {
	r := Report{Version: version.Version, SkillHash: Hash(), Harnesses: []HarnessStatus{}}
	for _, h := range Harnesses {
		det, _ := h.Detect(home)
		p := h.Path(home)
		state, ver := stateOf(p)
		r.Harnesses = append(r.Harnesses, HarnessStatus{Name: h.Name, Detected: det, Path: p, State: state, Installed: ver})
	}
	return r
}

// Options picks harnesses for install and uninstall.
type Options struct {
	Home      string
	Harnesses []string // empty: detected ones (install) or all (uninstall)
	DryRun    bool
}

// Result is what happened to one skill folder, shared by every harness in Names.
type Result struct {
	Names  []string
	Path   string
	Action string // installed, updated, reinstalled, would install, skipped, left alone, removed, not installed, would remove
	Detail string
	Err    error
}

func (o Options) pick(all bool) ([]Harness, []Result, error) {
	home := o.Home
	if home == "" {
		home = Home()
	}
	if len(o.Harnesses) > 0 {
		var hs []Harness
		seen := map[string]bool{}
		for _, n := range o.Harnesses {
			h, ok := Lookup(strings.TrimSpace(n))
			if !ok {
				return nil, nil, fmt.Errorf("unknown harness %q (want one of %s)", n, strings.Join(Names(), ", "))
			}
			if !seen[h.Name] {
				seen[h.Name] = true
				hs = append(hs, h)
			}
		}
		return hs, nil, nil
	}
	if all {
		return Harnesses, nil, nil
	}
	var hs []Harness
	var skipped []Result
	for _, h := range Harnesses {
		if ok, _ := h.Detect(home); ok {
			hs = append(hs, h)
		} else {
			skipped = append(skipped, Result{Names: []string{h.Name}, Path: h.Path(home), Action: "skipped",
				Detail: fmt.Sprintf("not found (no %s on PATH, no ~/%s)", h.Bins[0], h.Config)})
		}
	}
	return hs, skipped, nil
}

// group merges harnesses that share a skills folder, in table order.
func group(hs []Harness, home string) []Result {
	var out []Result
	idx := map[string]int{}
	for _, h := range hs {
		p := h.Path(home)
		if i, ok := idx[p]; ok {
			out[i].Names = append(out[i].Names, h.Name)
			continue
		}
		idx[p] = len(out)
		out = append(out, Result{Names: []string{h.Name}, Path: p})
	}
	return out
}

// Install copies the skill into each picked harness's folder.
func Install(o Options) ([]Result, error) {
	if o.Home == "" {
		o.Home = Home()
	}
	hs, skipped, err := o.pick(false)
	if err != nil {
		return nil, err
	}
	out := group(hs, o.Home)
	for i := range out {
		r := &out[i]
		state, ver := stateOf(r.Path)
		switch state {
		case StateForeign:
			r.Action, r.Detail = "left alone", "a symlink or file is in the way; yonder replaces only its own folder"
			continue
		case StateMissing:
			r.Action = "installed"
		case StateOutdated:
			r.Action = "updated"
			if ver != "" {
				r.Detail = "was " + ver
			}
		case StateCurrent:
			r.Action = "reinstalled"
			r.Detail = "already current"
		}
		if o.DryRun {
			r.Action = "would install"
			continue
		}
		if err := writeSkill(r.Path); err != nil {
			r.Err = err
		}
	}
	return append(out, skipped...), nil
}

// Uninstall removes the skill folder for each picked harness (all by default).
func Uninstall(o Options) ([]Result, error) {
	if o.Home == "" {
		o.Home = Home()
	}
	hs, _, err := o.pick(true)
	if err != nil {
		return nil, err
	}
	out := group(hs, o.Home)
	for i := range out {
		r := &out[i]
		switch state, _ := stateOf(r.Path); state {
		case StateMissing:
			r.Action = "not installed"
		case StateForeign:
			r.Action, r.Detail = "left alone", "a symlink or file, not yonder's folder"
		default:
			if o.DryRun {
				r.Action = "would remove"
				continue
			}
			r.Action = "removed"
			r.Err = os.RemoveAll(r.Path)
		}
	}
	return out, nil
}

// writeSkill replaces dest with a fresh copy: build it in a temp dir beside
// dest, then swap it in with renames.
func writeSkill(dest string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	cleanLeftovers(parent)
	tmp, err := os.MkdirTemp(parent, ".yonder-tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	err = fs.WalkDir(skill.FS, folder, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(folder, p)
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := skill.FS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, versionFile), []byte(stamp()), 0o644); err != nil {
		return err
	}
	old := ""
	if _, err := os.Lstat(dest); err == nil {
		old = filepath.Join(parent, fmt.Sprintf(".yonder-old-%d", time.Now().UnixNano()))
		if err := os.Rename(dest, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		if old != "" {
			_ = os.Rename(old, dest)
		}
		return err
	}
	if old != "" {
		return os.RemoveAll(old)
	}
	return nil
}

// cleanLeftovers drops temp folders a crashed install left behind.
func cleanLeftovers(parent string) {
	for _, pat := range []string{".yonder-tmp-*", ".yonder-old-*"} {
		ms, _ := filepath.Glob(filepath.Join(parent, pat))
		for _, m := range ms {
			if st, err := os.Lstat(m); err == nil && time.Since(st.ModTime()) > 10*time.Minute {
				os.RemoveAll(m)
			}
		}
	}
}

// Tilde shortens a path under home to ~/...
func Tilde(p, home string) string {
	if home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return p
}

// Print writes one line per result.
func Print(w io.Writer, rs []Result, home string) (failed bool) {
	sort.SliceStable(rs, func(i, j int) bool { return rank(rs[i]) < rank(rs[j]) })
	for _, r := range rs {
		names := strings.Join(r.Names, ", ")
		line := fmt.Sprintf("skill %s for %s: %s", r.Action, names, Tilde(r.Path, home))
		if r.Action == "skipped" {
			line = fmt.Sprintf("skill skipped for %s: %s", names, r.Detail)
		} else if r.Detail != "" {
			line += " (" + r.Detail + ")"
		}
		// a symlinked skills dir (dotfiles, a synced folder) means the write lands elsewhere
		if dir := filepath.Dir(r.Path); r.Action != "skipped" {
			if st, err := os.Lstat(dir); err == nil && st.Mode()&os.ModeSymlink != 0 {
				if real, err := filepath.EvalSymlinks(dir); err == nil {
					realHome, _ := filepath.EvalSymlinks(home)
					line += "\n  note: " + Tilde(dir, home) + " is a link to " + Tilde(real, realHome) + ", so the skill lives there"
				}
			}
		}
		if r.Err != nil {
			line = fmt.Sprintf("skill for %s: failed: %v", names, r.Err)
			failed = true
		}
		fmt.Fprintln(w, line)
	}
	return failed
}

func rank(r Result) int {
	if r.Action == "skipped" {
		return 1
	}
	return 0
}

// InstallAll is the init hook: install for every detected harness.
func InstallAll() error {
	home := Home()
	rs, err := Install(Options{Home: home})
	if err != nil {
		return err
	}
	if Print(os.Stdout, rs, home) {
		return fmt.Errorf("some skill installs failed; `yonder agents install` retries")
	}
	return nil
}
