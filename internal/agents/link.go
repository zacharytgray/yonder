package agents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/store"
)

// what each harness gets from the store. key names its overlay files
// (instructions.<key>.md, files/<key>/) and claude's own folder.
type wiring struct {
	key          string
	instructions string // file under ~ that gets the instructions
	wholeFile    bool   // the file is yonder's when missing or rendered before; else a marked block
	skills       string // folder under ~ that gets one link per skill; "" for none
	files        string // folder under ~ that files/<key>/* link into
	claudeDirs   bool   // also claude/agents, claude/commands and hooks
	note         string
}

var wirings = map[string]wiring{
	"claude-code": {key: "claude", instructions: ".claude/CLAUDE.md", skills: ".claude/skills", files: ".claude", claudeDirs: true},
	"codex":       {key: "codex", instructions: ".codex/AGENTS.md", wholeFile: true, skills: ".agents/skills", files: ".codex"},
	"pi":          {key: "pi", instructions: ".pi/agent/AGENTS.md", wholeFile: true, skills: ".agents/skills", files: ".pi/agent"},
	"hermes": {key: "hermes", files: ".hermes",
		note: "hermes: yonder leaves its config alone; to give it the shared skills, add %s to skills.external_dirs in ~/.hermes/config.yaml"},
}

// entry kinds
const (
	kindLink   = "link"   // a symlink into the store
	kindBlock  = "block"  // a marked block in a file the user owns
	kindRender = "render" // a whole file yonder renders
	kindDir    = "dir"    // a real folder that replaced a symlinked one
)

// Entry is one thing link made, and what it replaced.
type Entry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Target  string `json:"target,omitempty"`   // a link's store path
	Backup  string `json:"backup,omitempty"`   // what was at Path, moved here
	OldLink string `json:"old_link,omitempty"` // Path was a symlink to this
	Created bool   `json:"created,omitempty"`  // Path didn't exist before
	Wrote   string `json:"wrote,omitempty"`    // sha256 of the file as yonder last wrote it
	Carry   bool   `json:"carry,omitempty"`    // a link to what a replaced symlinked folder held
}

// LinkState is <state>/agents/links.json.
type LinkState struct {
	Harnesses []string `json:"harnesses"`
	Paused    bool     `json:"paused,omitempty"` // the layer is off; link again when it is back on
	Entries   []Entry  `json:"entries"`
}

// Linker wires the agents store into the harnesses on this machine.
type Linker struct {
	Home     string // ~
	StoreDir string
	StateDir string // <state>/agents
	Self     string
	DryRun   bool
	Replace  bool // replace symlinks in the way (the old target is kept for unlink)
	Out      io.Writer
}

// NewLinker is the linker at the standard paths.
func NewLinker(self string, out io.Writer) *Linker {
	return &Linker{
		Home: Home(), StoreDir: filepath.Join(registry.DataDir(), StoreName),
		StateDir: filepath.Join(registry.StateDir(), "agents"), Self: self, Out: out,
	}
}

func (l *Linker) printf(format string, args ...any) {
	if l.Out != nil {
		fmt.Fprintf(l.Out, format+"\n", args...)
	}
}

func (l *Linker) statePath() string { return filepath.Join(l.StateDir, "links.json") }

// State reads links.json; missing is empty.
func (l *Linker) State() (LinkState, error) {
	var st LinkState
	data, err := os.ReadFile(l.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(data, &st)
}

func (l *Linker) save(st LinkState) error {
	if l.DryRun {
		return nil
	}
	if len(st.Entries) == 0 && len(st.Harnesses) == 0 {
		err := os.Remove(l.statePath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(l.StateDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(l.statePath(), data, 0o600)
}

func (l *Linker) tilde(p string) string { return Tilde(p, l.Home) }

// want is one thing link should make.
type want struct {
	path, kind, target, content string
}

// item is one store entry with its overlay applied.
type item struct{ name, path string }

// items lists sub's entries, with machines/<self>/sub overriding by name.
func (l *Linker) items(sub string, dirs bool) []item {
	byName := map[string]string{}
	for _, base := range []string{l.StoreDir, filepath.Join(l.StoreDir, "machines", l.Self)} {
		es, err := os.ReadDir(filepath.Join(base, sub))
		if err != nil {
			continue
		}
		for _, e := range es {
			n := e.Name()
			if strings.HasPrefix(n, ".") || e.IsDir() != dirs {
				continue
			}
			if _, _, conflict := store.ConflictOf(n); conflict {
				continue
			}
			if !dirs && !e.Type().IsRegular() {
				continue
			}
			// a name that never syncs is never linked either: a settings file
			// under files/, a secret, yonder's own skill
			rel, _ := filepath.Rel(l.StoreDir, filepath.Join(base, sub, n))
			if r := Check(filepath.ToSlash(rel), nil); r != nil && r.Final {
				continue
			}
			byName[n] = filepath.Join(base, sub, n)
		}
	}
	var out []item
	for n, p := range byName {
		out = append(out, item{n, p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// instructionFiles are the shared text, the harness overlay, and the
// same two for this machine, in that order, where they exist.
func (l *Linker) instructionFiles(key string) []string {
	var out []string
	for _, base := range []string{l.StoreDir, filepath.Join(l.StoreDir, "machines", l.Self)} {
		for _, n := range []string{"instructions.md", "instructions." + key + ".md"} {
			p := filepath.Join(base, n)
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				out = append(out, p)
			}
		}
	}
	return out
}

// renderText is the shared text, the harness overlay and the machine's two,
// joined with blank lines. empty when the store has none.
func (l *Linker) renderText(key string) string {
	var parts []string
	for _, f := range l.instructionFiles(key) {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		// text that can't sync isn't rendered either (a key, a block marker)
		rel, _ := filepath.Rel(l.StoreDir, f)
		if r := Check(filepath.ToSlash(rel), data); r != nil && r.Final {
			continue
		}
		if t := strings.TrimSpace(string(data)); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// BlockWarn is the rendered text size doctor warns past. there is no hard
// limit, but every session loads all of it.
const BlockWarn = 25 << 10

const (
	blockBegin = "<!-- yonder:begin (written by `yonder agents link`; edit the store, not this block) -->"
	blockEnd   = "<!-- yonder:end -->"
	renderMark = "<!-- yonder: rendered by `yonder agents link` from the agents store; edit the store, not this file -->"
)

func (l *Linker) wants(harnesses []string) ([]want, []string) {
	var out []want
	var notes []string
	seen := map[string]bool{}
	add := func(w want) {
		if !seen[w.path] {
			seen[w.path] = true
			out = append(out, w)
		}
	}
	for _, h := range harnesses {
		w, ok := wirings[h]
		if !ok {
			continue
		}
		if w.instructions != "" {
			// the text itself, never an @import: claude code asks once per project
			// before importing a file outside it, which a headless session can't answer
			if text := l.renderText(w.key); text != "" {
				kind := kindBlock
				if w.wholeFile {
					kind = kindRender
				}
				add(want{path: filepath.Join(l.Home, w.instructions), kind: kind, content: text})
			}
		}
		if w.skills != "" {
			for _, it := range l.items("skills", true) {
				add(want{path: filepath.Join(l.Home, w.skills, it.name), kind: kindLink, target: it.path})
			}
		}
		if w.claudeDirs {
			for _, d := range []struct{ sub, dest string }{
				{"claude/agents", ".claude/agents"}, {"claude/commands", ".claude/commands"}, {"hooks", ".claude/hooks"},
			} {
				for _, it := range l.items(d.sub, false) {
					add(want{path: filepath.Join(l.Home, d.dest, it.name), kind: kindLink, target: it.path})
				}
			}
		}
		if w.files != "" {
			for _, it := range l.items(filepath.Join("files", w.key), false) {
				add(want{path: filepath.Join(l.Home, w.files, it.name), kind: kindLink, target: it.path})
			}
		}
		if w.note != "" {
			notes = append(notes, fmt.Sprintf(w.note, filepath.Join(l.StoreDir, "skills")))
		}
	}
	return out, notes
}

// Link wires the store into the harnesses: the instructions rendered into a
// marked block (Claude Code) or a rendered AGENTS.md (Codex, Pi), and one symlink per skill, subagent, command,
// hook and passthrough file. settings files are never touched. anything in the
// way is backed up first, and unlink puts it back. it is safe to run again.
func (l *Linker) Link(harnesses []string) error {
	return l.locked(func() error { return l.link(harnesses) })
}

func (l *Linker) link(harnesses []string) error {
	st, err := l.State()
	if err != nil {
		return err
	}
	if harnesses == nil {
		harnesses = st.Harnesses
	}
	wants, notes := l.wants(harnesses)
	wanted := map[string]want{}
	for _, w := range wants {
		wanted[w.path] = w
	}
	// drop what is no longer wanted, links before folders
	var keep []Entry
	for _, e := range sortForRemoval(st.Entries) {
		w, ok := wanted[e.Path]
		if ok && (w.kind == e.Kind || (e.Kind == kindBlock && w.kind == kindRender)) && (e.Kind != kindLink || w.target == e.Target) {
			keep = append(keep, e)
			continue
		}
		// folders that replaced a symlink, and the links carried into them, stay until unlink
		if e.Kind == kindDir || e.Carry {
			keep = append(keep, e)
			continue
		}
		if err := l.remove(e); err != nil {
			l.printf("could not undo %s: %v", l.tilde(e.Path), err)
			keep = append(keep, e)
		}
	}
	st.Entries = keep
	st.Harnesses = harnesses
	var failed []string
	for _, w := range wants {
		es, err := l.make(w, &st)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", l.tilde(w.path), err))
			continue
		}
		st.Entries = es
	}
	for _, n := range notes {
		l.printf("%s", n)
	}
	if err := l.save(st); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not link %s", strings.Join(failed, "; "))
	}
	return nil
}

func sortForRemoval(es []Entry) []Entry {
	out := append([]Entry(nil), es...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Kind != kindDir && out[j].Kind == kindDir })
	return out
}

func findEntry(es []Entry, path string) int {
	for i, e := range es {
		if e.Path == path {
			return i
		}
	}
	return -1
}

func isLink(p string) (string, bool) {
	t, err := os.Readlink(p)
	return t, err == nil
}

// backup moves p into a fresh backup folder, keeping its place under ~.
func (l *Linker) backup(p string) (string, error) {
	rel, err := filepath.Rel(l.Home, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = filepath.Base(p)
	}
	dest := filepath.Join(l.StateDir, "backup", strconv.FormatInt(time.Now().UnixNano(), 10), rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	return dest, os.Rename(p, dest)
}

// copyBackup copies a file into a backup folder, leaving it in place.
func (l *Linker) copyBackup(p string) (string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(l.Home, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = filepath.Base(p)
	}
	dest := filepath.Join(l.StateDir, "backup", strconv.FormatInt(time.Now().UnixNano(), 10), rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	return dest, os.WriteFile(dest, data, 0o600)
}

// parentOK makes sure the folder p goes in is a real folder under ~. a
// symlinked folder is replaced only with Replace, and recorded.
func (l *Linker) parentOK(p string, st *LinkState) error {
	dir := filepath.Dir(p)
	rel, err := filepath.Rel(l.Home, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is outside the home folder", dir)
	}
	cur := l.Home
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		target, link := isLink(cur)
		if !link {
			continue
		}
		if !l.Replace {
			return fmt.Errorf("%s is a symlink to %s, and yonder doesn't write through it; rerun with --replace-links to replace it with a real folder (unlink puts the link back)", l.tilde(cur), target)
		}
		if l.DryRun {
			l.printf("would replace the symlink %s (to %s) with a real folder", l.tilde(cur), target)
			return nil
		}
		if err := os.Remove(cur); err != nil {
			return err
		}
		if err := os.Mkdir(cur, 0o755); err != nil {
			_ = os.Symlink(target, cur)
			return err
		}
		st.Entries = append(st.Entries, Entry{Path: cur, Kind: kindDir, OldLink: target})
		l.printf("replaced the symlink %s (to %s) with a real folder", l.tilde(cur), target)
		l.carry(cur, target, st)
	}
	if l.DryRun {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

// carry links each entry of a replaced symlinked folder's target into the
// new real folder, so the harness keeps loading the user's own skills and
// files. a name the store also has is left to the store, with a warning.
func (l *Linker) carry(dir, target string, st *LinkState) {
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(dir), target)
	}
	wants, _ := l.wants(st.Harnesses)
	es, err := os.ReadDir(target)
	if err != nil {
		l.printf("warning: could not read %s (%v); the harness no longer sees what it held", target, err)
		return
	}
	yonderCarried := false
	for _, e := range es {
		p := filepath.Join(dir, e.Name())
		shadowed := false
		for _, w := range wants {
			if w.path == p {
				shadowed = true
			}
		}
		if shadowed {
			l.printf("warning: the store's %s takes the place of %s", e.Name(), filepath.Join(target, e.Name()))
			continue
		}
		if err := os.Symlink(filepath.Join(target, e.Name()), p); err != nil {
			l.printf("warning: could not carry %s: %v", e.Name(), err)
			continue
		}
		yonderCarried = yonderCarried || e.Name() == folder
		st.Entries = append(st.Entries, Entry{Path: p, Kind: kindLink, Target: filepath.Join(target, e.Name()), Carry: true})
	}
	l.printf("carried %d entries of %s into it, as links", len(es), target)
	if !yonderCarried {
		l.reinstallSkill(dir)
	}
}

// reinstallSkill puts yonder's own skill back when its skills folder was a
// symlink that got replaced.
func (l *Linker) reinstallSkill(dir string) {
	for _, h := range Harnesses {
		if filepath.Join(l.Home, h.Skills) == dir {
			if err := writeSkill(h.Path(l.Home)); err == nil {
				l.printf("reinstalled the yonder skill in %s", l.tilde(dir))
			}
			return
		}
	}
}

// make makes one wanted thing and returns the updated entries.
func (l *Linker) make(w want, st *LinkState) ([]Entry, error) {
	if err := l.parentOK(w.path, st); err != nil {
		return st.Entries, err
	}
	es := st.Entries // parentOK may have added a folder entry
	i := findEntry(es, w.path)
	switch w.kind {
	case kindLink:
		if t, ok := isLink(w.path); ok && t == w.target {
			if i < 0 {
				es = append(es, Entry{Path: w.path, Kind: kindLink, Target: w.target})
			}
			return es, nil
		}
		e := Entry{Path: w.path, Kind: kindLink, Target: w.target}
		if i >= 0 {
			e.Backup = es[i].Backup
		}
		if l.DryRun {
			l.printf("would link %s -> %s", l.tilde(w.path), l.tilde(w.target))
			return es, nil
		}
		if _, err := os.Lstat(w.path); err == nil {
			if t, link := isLink(w.path); link && i >= 0 && t == es[i].Target {
				if err := os.Remove(w.path); err != nil { // our own older link
					return es, err
				}
			} else {
				// anything else, even at a path link made: an editor's save by
				// rename leaves a real file where the link was
				b, err := l.backup(w.path)
				if err != nil {
					return es, err
				}
				if e.Backup == "" {
					e.Backup = b
				}
				l.printf("backed up %s to %s", l.tilde(w.path), b)
			}
		}
		if err := os.Symlink(w.target, w.path); err != nil {
			return es, err
		}
		l.printf("linked %s -> %s", l.tilde(w.path), l.tilde(w.target))
		if i >= 0 {
			es[i] = e
		} else {
			es = append(es, e)
		}
		return es, nil
	case kindBlock, kindRender:
		return l.writeText(w, es, i)
	}
	return es, fmt.Errorf("unknown kind %s", w.kind)
}

// writeText writes an instructions file. yonder owns the whole file only
// when it is missing or starts with yonder's marker; anything else is the
// user's, and gets a marked block. the decision is made from the file each
// time, never from what link wrote before. a file yonder didn't write last is
// backed up before every change.
func (l *Linker) writeText(w want, es []Entry, i int) ([]Entry, error) {
	var e Entry
	if i >= 0 {
		e = es[i]
	} else {
		e = Entry{Path: w.path, Kind: w.kind}
	}
	var old []byte
	exists, replacedLink := false, false
	if t, ok := isLink(w.path); ok {
		if !l.Replace {
			return es, fmt.Errorf("it is a symlink to %s, and yonder doesn't write through it; rerun with --replace-links to replace the link with a real file (unlink puts it back)", t)
		}
		// the user's own text comes along, so the harness still reads it
		text, err := os.ReadFile(w.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return es, err
		}
		if l.DryRun {
			l.printf("would replace the symlink %s (to %s) with a file holding its text and the yonder block", l.tilde(w.path), t)
			return es, nil
		}
		if err := os.Remove(w.path); err != nil {
			return es, err
		}
		e.OldLink, e.Created, e.Wrote = t, true, ""
		old, exists, replacedLink = text, true, true
		l.printf("replaced the symlink %s (to %s) with a file holding its text", l.tilde(w.path), t)
	} else {
		data, err := os.ReadFile(w.path)
		exists = err == nil
		if !exists && !errors.Is(err, os.ErrNotExist) {
			return es, err
		}
		old = data
	}
	if !exists && i < 0 {
		e.Created = true
	}
	var next string
	if w.kind == kindRender && (!exists || strings.HasPrefix(string(old), renderMark)) {
		e.Kind = kindRender
		next = renderMark + "\n\n" + w.content
	} else {
		e.Kind = kindBlock
		n, err := withBlock(string(old), w.content)
		if err != nil {
			return es, err
		}
		next = n
	}
	if exists && !replacedLink && string(old) == next {
		e.Wrote = hashText(next)
		if i < 0 {
			es = append(es, e)
		} else {
			es[i] = e
		}
		return es, nil
	}
	if l.DryRun {
		l.printf("would write %s", l.tilde(w.path))
		return es, nil
	}
	if exists && !replacedLink && hashText(string(old)) != e.Wrote {
		b, err := l.copyBackup(w.path)
		if err != nil {
			return es, err
		}
		if e.Backup == "" {
			e.Backup = b
		}
		l.printf("backed up %s to %s", l.tilde(w.path), b)
	}
	if err := writeFileAtomic(w.path, []byte(next)); err != nil {
		return es, err
	}
	e.Wrote = hashText(next)
	if e.Kind == kindBlock {
		l.printf("wrote the yonder block in %s", l.tilde(w.path))
	} else {
		l.printf("rendered %s", l.tilde(w.path))
	}
	if i >= 0 {
		es[i] = e
	} else {
		es = append(es, e)
	}
	return es, nil
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

const beginPrefix = "<!-- yonder:begin"

// errOpenBlock: a begin marker with no end marker after it. yonder can't
// tell where its block stops, so it changes nothing.
var errOpenBlock = errors.New("it has a yonder begin marker with no end marker after it, so yonder can't tell where its block stops; delete the begin line, or add `" + blockEnd + "` where the block ends, then run `yonder agents link`")

// splitBlock finds the block: the first begin marker to the last end marker.
func splitBlock(text string) (before, after string, found bool, err error) {
	b := strings.Index(text, beginPrefix)
	if b < 0 {
		return text, "", false, nil
	}
	rest := text[b:]
	e := strings.LastIndex(rest, blockEnd)
	if e < 0 {
		return "", "", false, errOpenBlock
	}
	return text[:b], strings.TrimPrefix(rest[e+len(blockEnd):], "\n"), true, nil
}

// withBlock puts content between the markers, replacing an existing block
// or appending one.
func withBlock(text, content string) (string, error) {
	block := blockBegin + "\n" + content + blockEnd + "\n"
	before, after, found, err := splitBlock(text)
	if err != nil {
		return "", err
	}
	if found {
		return before + block + after, nil
	}
	if text == "" {
		return block, nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + "\n" + block, nil
}

// withoutBlock removes the marked block and the blank line before it.
func withoutBlock(text string) (string, error) {
	before, after, found, err := splitBlock(text)
	if err != nil || !found {
		return text, err
	}
	before = strings.TrimRight(before, "\n")
	switch {
	case before == "":
		return after, nil
	case after == "":
		return before + "\n", nil
	}
	return before + "\n\n" + after, nil
}

func writeFileAtomic(p string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := filepath.Join(filepath.Dir(p), ".yonder-tmp-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// remove undoes one entry and puts back what it replaced. it removes only
// what yonder can tell is its own: a symlink to the recorded target, a file
// that starts with its marker, the text between its markers.
func (l *Linker) remove(e Entry) error {
	if l.DryRun {
		l.printf("would undo %s", l.tilde(e.Path))
		return nil
	}
	switch e.Kind {
	case kindLink:
		if t, ok := isLink(e.Path); ok && t == e.Target {
			if err := os.Remove(e.Path); err != nil {
				return err
			}
			if !e.Carry {
				l.printf("unlinked %s", l.tilde(e.Path))
			}
		}
		if e.Backup != "" {
			if _, err := os.Lstat(e.Path); errors.Is(err, os.ErrNotExist) {
				if err := os.Rename(e.Backup, e.Path); err != nil {
					return err
				}
				l.printf("put back %s", l.tilde(e.Path))
			}
		}
	case kindBlock, kindRender:
		data, err := os.ReadFile(e.Path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return err
		case strings.HasPrefix(string(data), renderMark):
			if hashText(string(data)) != e.Wrote {
				b, err := l.copyBackup(e.Path)
				if err != nil {
					return err
				}
				l.printf("backed up %s to %s (it was changed after yonder wrote it)", l.tilde(e.Path), b)
			}
			if err := os.Remove(e.Path); err != nil {
				return err
			}
			l.printf("removed %s", l.tilde(e.Path))
		case strings.Contains(string(data), beginPrefix):
			next, err := withoutBlock(string(data))
			if err != nil {
				return err
			}
			if strings.TrimSpace(next) == "" && e.Created && e.OldLink == "" {
				if err := os.Remove(e.Path); err != nil {
					return err
				}
				l.printf("removed %s", l.tilde(e.Path))
			} else if next != string(data) {
				if err := writeFileAtomic(e.Path, []byte(next)); err != nil {
					return err
				}
				l.printf("took the yonder block out of %s", l.tilde(e.Path))
			}
		default:
			l.printf("left %s alone: it no longer has yonder's marker", l.tilde(e.Path))
		}
		if e.OldLink != "" {
			if err := l.restoreFileLink(e); err != nil {
				return err
			}
		}
		return nil
	case kindDir:
		// the yonder skill reinstalled here is ours too
		if _, _, ok := readStamp(filepath.Join(e.Path, folder)); ok {
			os.RemoveAll(filepath.Join(e.Path, folder))
		}
		if err := os.Remove(e.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s still holds files; move them, then `yonder agents unlink` again: %w", l.tilde(e.Path), err)
		}
	}
	if e.OldLink != "" {
		if _, err := os.Lstat(e.Path); errors.Is(err, os.ErrNotExist) {
			if err := os.Symlink(e.OldLink, e.Path); err != nil {
				return err
			}
			l.printf("put back the symlink %s -> %s", l.tilde(e.Path), e.OldLink)
		}
	}
	return nil
}

// restoreFileLink puts back a symlinked instructions file that link replaced
// with a copy of its text. the copy, now without the block, goes; if it no
// longer matches the link's target (edited since), it is backed up first.
func (l *Linker) restoreFileLink(e Entry) error {
	cur, err := os.ReadFile(e.Path)
	if err == nil {
		target := e.OldLink
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(e.Path), target)
		}
		orig, _ := os.ReadFile(target)
		if !bytes.Equal(cur, orig) {
			b, err := l.copyBackup(e.Path)
			if err != nil {
				return err
			}
			l.printf("backed up %s to %s (it differs from %s)", l.tilde(e.Path), b, e.OldLink)
		}
		if err := os.Remove(e.Path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(e.OldLink, e.Path); err != nil {
		return err
	}
	l.printf("put back the symlink %s -> %s", l.tilde(e.Path), e.OldLink)
	return nil
}

// Unlink undoes everything link made and puts back what it replaced.
// pause keeps the harness list, so turning the layer back on links again.
func (l *Linker) Unlink(pause bool) error {
	return l.locked(func() error { return l.unlink(pause) })
}

func (l *Linker) unlink(pause bool) error {
	st, err := l.State()
	if err != nil {
		return err
	}
	var left []Entry
	var errs []string
	for _, e := range sortForRemoval(st.Entries) {
		if err := l.remove(e); err != nil {
			left = append(left, e)
			errs = append(errs, fmt.Sprintf("%s: %v", l.tilde(e.Path), err))
		}
	}
	st.Entries = left
	if pause {
		st.Paused = len(st.Harnesses) > 0
	} else {
		st.Harnesses, st.Paused = nil, false
	}
	if err := l.save(st); err != nil {
		return err
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Linked reports whether link has run here and isn't paused.
func (l *Linker) Linked() bool {
	st, err := l.State()
	return err == nil && !st.Paused && len(st.Harnesses) > 0
}

// ApplyLayer does what the agents layer's setting asks here: off unlinks and
// pauses; on links again where it was paused.
func ApplyLayer(on bool, self string, out io.Writer) error {
	l := NewLinker(self, out)
	return l.locked(func() error {
		st, err := l.State()
		if err != nil {
			return err
		}
		switch {
		case !on && len(st.Entries) > 0:
			return l.unlink(true)
		case on && st.Paused:
			st.Paused = false
			if err := l.save(st); err != nil {
				return err
			}
			return l.link(st.Harnesses)
		}
		return nil
	})
}

// how long link waits for another yonder's link to finish. a var for tests.
var linkLockWait = 30 * time.Second

// locked runs fn holding links.lock, so a sync's relink and a layer change
// arriving through _sync never edit links.json at once.
func (l *Linker) locked(fn func() error) error {
	if l.DryRun {
		return fn()
	}
	if err := os.MkdirAll(l.StateDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(l.StateDir, "links.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	deadline := time.Now().Add(linkLockWait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			return fmt.Errorf("links are busy: another yonder is linking here: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// afterChange relinks after a sync changed the store, so new skills get
// links and the rendered files pick up new text.
func afterChange(s *store.Store, _ []string) {
	l := NewLinker(s.Self, nil)
	l.StoreDir = s.Dir
	if l.Linked() {
		_ = l.Link(nil)
	}
}

// upToDate: the file holds exactly the rendered text.
func upToDate(data, kind, content string) bool {
	if kind == kindRender {
		return data == renderMark+"\n\n"+content
	}
	next, err := withBlock(data, content)
	return err == nil && next == data
}

// LargeTexts names the rendered instructions past BlockWarn, with their size.
func (l *Linker) LargeTexts() []string {
	st, err := l.State()
	if err != nil || st.Paused {
		return nil
	}
	ws, _ := l.wants(st.Harnesses)
	var out []string
	for _, w := range ws {
		if (w.kind == kindBlock || w.kind == kindRender) && len(w.content) > BlockWarn {
			out = append(out, fmt.Sprintf("%s gets %d KB of instructions", l.tilde(w.path), len(w.content)>>10))
		}
	}
	return out
}

// Problems reports entries that no longer match what link made.
func (l *Linker) Problems() []string {
	st, err := l.State()
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	wanted := map[string]want{}
	if ws, _ := l.wants(st.Harnesses); !st.Paused {
		for _, w := range ws {
			wanted[w.path] = w
		}
	}
	for _, e := range st.Entries {
		if w, ok := wanted[e.Path]; ok && (e.Kind == kindBlock || e.Kind == kindRender) {
			if data, err := os.ReadFile(e.Path); err == nil && strings.Contains(string(data), "<!-- yonder:") && !upToDate(string(data), e.Kind, w.content) {
				out = append(out, l.tilde(e.Path)+" is behind the store's instructions (yonder sync re-renders it)")
			}
		}
		switch e.Kind {
		case kindLink:
			if t, ok := isLink(e.Path); !ok || t != e.Target {
				out = append(out, l.tilde(e.Path)+" no longer links into the store")
			} else if _, err := os.Stat(e.Path); err != nil {
				out = append(out, l.tilde(e.Path)+" links to "+l.tilde(e.Target)+", which is gone")
			}
		case kindBlock:
			data, err := os.ReadFile(e.Path)
			switch {
			case err != nil || !strings.Contains(string(data), beginPrefix):
				out = append(out, l.tilde(e.Path)+" lost its yonder block")
			default:
				if _, _, _, err := splitBlock(string(data)); err != nil {
					out = append(out, l.tilde(e.Path)+": "+err.Error())
				}
			}
		case kindRender:
			if data, err := os.ReadFile(e.Path); err != nil || !strings.HasPrefix(string(data), renderMark) {
				out = append(out, l.tilde(e.Path)+" was changed or removed")
			}
		}
	}
	return out
}
