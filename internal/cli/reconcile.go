package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/store"
)

func newAgentsReconcileCmd() *cobra.Command {
	var (
		planPath string
		dryRun   bool
		undo     string
	)
	cmd := &cobra.Command{
		Use:   "reconcile --plan <file> [--dry-run] | --undo [id]",
		Short: "Apply a plan that settles how the machines' instructions differ",
		Long: `Apply a reconcile plan: per section that ` + "`yonder agents gather --json`" + ` found, what to do
with it. An agent writes the plan with the user (the yonder skill has the steps).

  {"v": 1, "fingerprint": "<from gather --json>", "sections": [
    {"key": "## style",  "action": "shared", "from": "laptop"},
    {"key": "## gpu",    "action": "per-machine"},
    {"key": "## models", "action": "merge", "text": "## Models\n..."},
    {"key": "## old",    "action": "drop"},
    {"key": "## mine",   "action": "leave"}]}

  shared       one machine's version goes in instructions.md, for every machine ("from":
               a machine, or machine:~/path when that machine has two versions)
  per-machine  each machine keeps its own version, in machines/<m>/instructions.md
               ("machines": [...] limits it to some)
  merge        "text" goes in instructions.md for every machine
  drop         taken out everywhere
  leave        stays in each machine's own file (also any section the plan leaves out)

Applying writes the store files, syncs them, then on each machine backs up its own
instructions files, takes out the sections that moved, and links the store in so the
moved text arrives in yonder's block. --dry-run shows all of that and writes nothing.
--undo puts every file back (the last reconcile, or the id it printed).`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			switch {
			case cmd.Flags().Changed("undo") && planPath != "":
				return &usageError{errors.New("--plan or --undo, not both")}
			case cmd.Flags().Changed("undo"):
				return a.reconcileUndo(undo)
			case planPath == "":
				return &usageError{errors.New("--plan <file> is required (or --undo); see `yonder agents reconcile --help`")}
			}
			return a.reconcileApply(planPath, dryRun)
		},
	}
	cmd.Flags().StringVar(&planPath, "plan", "", "the plan file (- for stdin)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change; write nothing")
	cmd.Flags().StringVar(&undo, "undo", "", "undo a reconcile: the last one, or this id")
	cmd.Flags().Lookup("undo").NoOptDefVal = "last"
	return cmd
}

// reconcileRecord is kept on the machine that applied a plan, to undo it.
type reconcileRecord struct {
	ID       string                `json:"id"`
	Time     time.Time             `json:"time"`
	Plan     agents.Plan           `json:"plan"`
	Store    map[string]storeEntry `json:"store"`
	Machines []string              `json:"machines"` // where files were trimmed
	Undone   bool                  `json:"undone,omitempty"`
}

type storeEntry struct {
	Before *string `json:"before"` // null: it didn't exist
	After  string  `json:"after"`
}

func reconcileDir() string { return filepath.Join(registry.StateDir(), "agents", "reconcile") }

func saveRecord(r reconcileRecord) error {
	data, _ := json.MarshalIndent(r, "", "  ")
	p := filepath.Join(reconcileDir(), r.ID, "record.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// trimRequest is what _reconcile-trim reads.
type trimRequest struct {
	ID    string            `json:"id"`
	Trims []agents.Trim     `json:"trims"`
	Store map[string]string `json:"store"` // store path -> the hash it must have here first
}

type trimDone struct {
	Trimmed []trimmedFile `json:"trimmed"`
	Skipped []skip        `json:"skipped"`
	Linked  string        `json:"linked,omitempty"`
	Error   string        `json:"error,omitempty"`
}

type trimmedFile struct {
	Path    string   `json:"path"`
	Removed []string `json:"removed"`
	Backup  string   `json:"backup"`
}

// what a machine keeps to undo its trims
type trimRecord struct {
	Files []trimFile `json:"files"`
}

type trimFile struct {
	Path   string `json:"path"` // under ~
	Before string `json:"before_hash"`
	After  string `json:"after_own_hash"` // the user's own text after the trim; link rewrites the block
	Backup string `json:"backup"`
}

func (a *app) reconcileApply(planPath string, dryRun bool) error {
	s, self, err := a.agentStore("agents reconcile")
	if err != nil {
		return err
	}
	var data []byte
	if planPath == "-" {
		data, err = io.ReadAll(io.LimitReader(a.in, 4<<20))
	} else {
		data, err = os.ReadFile(registry.ExpandHome(planPath))
	}
	if err != nil {
		return err
	}
	var plan agents.Plan
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return &usageError{fmt.Errorf("the plan isn't valid JSON for a reconcile plan: %w; see `yonder agents reconcile --help`", err)}
	}
	r, err := a.gather(self)
	if err != nil {
		return err
	}
	rc, err := agents.BuildReconcile(r, plan, func(rel string) (string, bool) {
		b, ok := s.Live(rel)
		return string(b), ok
	})
	if err != nil {
		return err
	}
	for rel, text := range rc.Store {
		if s.Kind.Check != nil {
			if ref := s.Kind.Check(rel, []byte(text)); ref != nil {
				return fmt.Errorf("%s would be refused by the store: %s; change that section's text in the plan", rel, ref.Reason)
			}
		}
	}
	// the new text must render for every harness before the store gets it
	if err := renderCheck(self, s, rc); err != nil {
		return err
	}
	for _, m := range r.Machines {
		if m.Error != "" {
			fmt.Fprintf(a.out, "%s: not read (%s); its files are left as they are; reconcile again once it is online\n", m.Name, m.Error)
		}
	}
	for _, l := range rc.Lines {
		fmt.Fprintln(a.out, l)
	}
	for _, rel := range sortedKeys(rc.Store) {
		cur, _ := s.Live(rel)
		fmt.Fprintf(a.out, "\nstore %s:\n", rel)
		fmt.Fprint(a.out, orNoDiff(agents.Diff(cur, []byte(rc.Store[rel]), "now", "after")))
	}
	machines := sortedKeys(rc.Trims)
	for _, m := range machines {
		for _, t := range rc.Trims[m] {
			fmt.Fprintf(a.out, "\n%s:~/%s: take out %s (backed up first)\n", m, t.Path, strings.Join(t.Remove, ", "))
		}
	}
	if len(rc.Store) == 0 && len(rc.Trims) == 0 {
		fmt.Fprintln(a.out, "\nthe plan changes nothing")
		return nil
	}
	if dryRun {
		fmt.Fprintln(a.out, "\ndry run: nothing written. Each machine's harnesses get the store text through `yonder agents link`, which applying runs there.")
		return nil
	}
	// every machine to trim must take it before anything is written
	for _, m := range machines {
		if m == self {
			continue
		}
		rec, err := a.store.Machine(m)
		if err != nil {
			return err
		}
		var out, errb bytes.Buffer
		code, err := a.execOn(rec, self, yonderCmd(rec, "_reconcile-trim", "--probe"), nil, &out, &errb)
		switch {
		case err != nil:
			return fmt.Errorf("%s can't be reached (%w); nothing was written; run this again when it is online", m, err)
		case code != 0:
			return fmt.Errorf("%s can't apply a reconcile (%s); upgrade yonder there to 0.4 or later; nothing was written", m, firstLine(errb.String()))
		}
	}
	id := time.Now().UTC().Format("20060102-150405") + "-" + randHex(3)
	rec := reconcileRecord{ID: id, Time: time.Now().UTC(), Plan: plan, Store: map[string]storeEntry{}}
	for rel, text := range rc.Store {
		e := storeEntry{After: text}
		if cur, ok := s.Live(rel); ok {
			c := string(cur)
			e.Before = &c
		}
		rec.Store[rel] = e
	}
	if err := saveRecord(rec); err != nil {
		return err
	}
	if len(rc.Store) > 0 {
		if _, err := withStore(s, func() error {
			for _, rel := range sortedKeys(rc.Store) {
				if err := s.Put(rel, []byte(rc.Store[rel]), false); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "\nwrote %s to the store\n", strings.Join(sortedKeys(rc.Store), ", "))
	}
	// always: a machine may still lack store text this plan didn't change
	a.syncStore(agents.StoreName, self, nil, syncOpts{quiet: true})
	failed := false
	for _, m := range machines {
		// every store file this machine's trims rely on, changed by this plan or not
		req := trimRequest{ID: id, Trims: rc.Trims[m], Store: map[string]string{}}
		for rel, text := range rc.Needs[m] {
			req.Store[rel] = hashHex(text)
		}
		var d trimDone
		if m == self {
			d = a.trimHere(self, req)
		} else {
			d = a.trimOn(m, self, req)
		}
		rec.Machines = append(rec.Machines, m)
		if !a.printTrim(m, d) {
			failed = true
		}
	}
	if err := saveRecord(rec); err != nil {
		return err
	}
	if failed {
		fmt.Fprintf(a.out, "\nreconcile %s NOT fully applied: a machine above refused or failed, and its files are unchanged. The store has the new text. Fix what is named above and run the plan again (gather first), or put every file back: yonder agents reconcile --undo %s\n", id, id)
		return quietExit(ExitError)
	}
	fmt.Fprintf(a.out, "\nreconcile %s applied. To put every file back: yonder agents reconcile --undo %s\n", id, id)
	return nil
}

// renderCheck renders the store as the plan leaves it, in a temp copy, for
// this machine and every machine the plan touches, with every harness. a
// section whose text won't render (a {{model:...}} with no name, say) is
// refused before anything is written.
func renderCheck(self string, s *store.Store, rc agents.Reconcile) error {
	if len(rc.Store) == 0 {
		return nil
	}
	tmp, err := os.MkdirTemp("", "yonder-render-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// only what renders into instructions: the texts, the briefing, render.toml
	err = filepath.WalkDir(s.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(s.Dir, p)
		top := !strings.Contains(rel, string(filepath.Separator))
		inMachine := strings.HasPrefix(rel, "machines"+string(filepath.Separator)) && strings.Count(rel, string(filepath.Separator)) == 2
		text := strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, ".toml")
		if (!top && !inMachine) || !text {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		return writeUnder(tmp, rel, data)
	})
	if err != nil {
		return err
	}
	for rel, text := range rc.Store {
		if err := writeUnder(tmp, filepath.FromSlash(rel), []byte(text)); err != nil {
			return err
		}
	}
	machines := []string{self}
	for m := range rc.Trims {
		if !slices.Contains(machines, m) {
			machines = append(machines, m)
		}
	}
	for rel := range rc.Store {
		if m, ok := strings.CutPrefix(rel, "machines/"); ok {
			if m, _, _ = strings.Cut(m, "/"); !slices.Contains(machines, m) {
				machines = append(machines, m)
			}
		}
	}
	sort.Strings(machines)
	for _, m := range machines {
		l := agents.NewLinker(m, io.Discard)
		l.StoreDir = tmp
		probs := l.RenderProblems(agents.Names())
		for _, p := range sortedKeys(probs) {
			return fmt.Errorf("the plan's text wouldn't render on %s (%s: %w); nothing was written. Change that section's text in the plan, or add the name to render.toml in the store", m, agents.Tilde(p, agents.Home()), probs[p])
		}
	}
	return nil
}

func writeUnder(root, rel string, data []byte) error {
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *app) printTrim(m string, d trimDone) bool {
	if d.Error != "" {
		fmt.Fprintf(a.out, "%s: nothing taken out: %s\n", m, agents.Printable(d.Error))
		return false
	}
	for _, t := range d.Trimmed {
		fmt.Fprintf(a.out, "%s: took %s out of ~/%s (backup %s)\n", m, strings.Join(t.Removed, ", "), t.Path, t.Backup)
	}
	for _, sk := range d.Skipped {
		fmt.Fprintf(a.out, "%s: left ~/%s alone: %s\n", m, sk.Path, agents.Printable(sk.Why))
	}
	if d.Linked != "" {
		fmt.Fprintf(a.out, "%s: %s\n", m, agents.Printable(d.Linked))
	}
	return len(d.Skipped) == 0
}

func (a *app) trimOn(m, self string, req trimRequest) trimDone {
	rec, err := a.store.Machine(m)
	if err != nil {
		return trimDone{Error: err.Error()}
	}
	body, _ := json.Marshal(req)
	var out, errb bytes.Buffer
	code, err := a.execOn(rec, self, yonderCmd(rec, "_reconcile-trim"), bytes.NewReader(body), &out, &errb)
	if err != nil {
		return trimDone{Error: err.Error()}
	}
	if code != 0 {
		return trimDone{Error: firstLine(errb.String())}
	}
	var d trimDone
	if err := json.Unmarshal(lastJSON(out.Bytes()), &d); err != nil {
		return trimDone{Error: "bad reply: " + err.Error()}
	}
	return d
}

// trimHere takes the moved sections out of this machine's own files, after
// checking that the store text they moved to is live here and renders, then
// links. if linking fails, every file it trimmed goes back.
func (a *app) trimHere(self string, req trimRequest) trimDone {
	d := trimDone{Trimmed: []trimmedFile{}, Skipped: []skip{}}
	if !validID(req.ID) {
		d.Error = "bad reconcile id"
		return d
	}
	s, err := store.Open(agents.StoreName, self)
	if err != nil {
		d.Error = err.Error()
		return d
	}
	storeReady := func() error {
		if err := s.Lock(30 * time.Second); err != nil {
			return err
		}
		defer s.Unlock()
		pending, _ := s.PendingList()
		for rel, h := range req.Store {
			live, ok := s.Live(rel)
			waits := slices.ContainsFunc(pending, func(it store.PendingItem) bool { return it.Path == rel })
			if waits || !ok || hashHex(string(live)) != h {
				return errors.New("the store's " + rel + " isn't the text this reconcile needs here yet (not synced, changed since, or waiting for review); nothing was taken out. Run `yonder sync`, accept it if it waits, then gather and reconcile again")
			}
		}
		return nil
	}
	if err := storeReady(); err != nil {
		d.Error = err.Error()
		return d
	}
	home := agents.Home()
	// the harnesses linked here keep their choice; a machine never linked gets what it has
	l := agents.NewLinker(self, io.Discard)
	hs, relink := []string(nil), false
	if st, err := l.State(); err == nil && len(st.Harnesses) > 0 && !st.Paused {
		hs, relink = st.Harnesses, true
	} else {
		hs, _ = pickHarnesses(nil)
	}
	// the store text must render for each harness before anything comes out
	for p, err := range l.RenderProblems(hs) {
		d.Error = fmt.Sprintf("%s wouldn't render from the store (%v); nothing was taken out. Fix the store text, then reconcile again", agents.Tilde(p, home), err)
		return d
	}
	dir := filepath.Join(reconcileDir(), req.ID)
	var tr trimRecord
	type undo struct {
		path string
		data []byte
		mode os.FileMode
	}
	var undos []undo
	for _, t := range req.Trims {
		gi := slices.IndexFunc(agents.GatherFiles, func(g agents.GatherFile) bool { return g.Path == t.Path })
		if store.ValidRel(t.Path) != nil || gi < 0 {
			d.Skipped = append(d.Skipped, skip{t.Path, "not an instructions file yonder reads"})
			continue
		}
		// only a harness that gets the store's text loses its own copy
		if h := agents.GatherFiles[gi].Harness; !slices.Contains(hs, h) {
			d.Skipped = append(d.Skipped, skip{t.Path, h + " isn't linked here, so the store's text wouldn't reach it; nothing taken out. Link it (`yonder agents link --harness " + h + "`), then gather and reconcile again"})
			continue
		}
		p := agents.InstructionsPath(home, t.Path)
		if err := noSymlinkOnWay(home, p); err != nil {
			d.Skipped = append(d.Skipped, skip{t.Path, err.Error() + "; take out " + strings.Join(t.Remove, ", ") + " by hand"})
			continue
		}
		st, err := os.Lstat(p)
		if err != nil {
			d.Skipped = append(d.Skipped, skip{t.Path, err.Error()})
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			d.Skipped = append(d.Skipped, skip{t.Path, err.Error()})
			continue
		}
		if ownHash(string(data)) != t.Hash {
			d.Skipped = append(d.Skipped, skip{t.Path, "your own text in it changed since gather read it; nothing taken out"})
			continue
		}
		next, removed, err := agents.TrimSections(string(data), t.Remove)
		if err != nil {
			d.Skipped = append(d.Skipped, skip{t.Path, err.Error()})
			continue
		}
		if len(removed) == 0 {
			continue
		}
		backup := filepath.Join(dir, "files", t.Path)
		err = os.MkdirAll(filepath.Dir(backup), 0o700)
		if err == nil {
			err = os.WriteFile(backup, data, 0o600)
		}
		// the store may have moved on since the check above: look again right before writing
		if err == nil {
			err = storeReady()
		}
		// never change the file without its backup
		if err == nil {
			err = writeKeepMode(p, []byte(next), st.Mode().Perm())
		}
		if err != nil {
			d.Skipped = append(d.Skipped, skip{t.Path, err.Error()})
			continue
		}
		undos = append(undos, undo{p, data, st.Mode().Perm()})
		tr.Files = append(tr.Files, trimFile{Path: t.Path, Before: t.Hash, After: ownHash(next), Backup: backup})
		d.Trimmed = append(d.Trimmed, trimmedFile{Path: t.Path, Removed: removed, Backup: agents.Tilde(backup, home)})
	}
	if len(tr.Files) == 0 {
		return d
	}
	putBack := func(why string) trimDone {
		for _, u := range undos {
			_ = writeKeepMode(u.path, u.data, u.mode)
		}
		return trimDone{Trimmed: []trimmedFile{}, Skipped: d.Skipped, Error: why + "; every file it trimmed was put back"}
	}
	// the moved text now comes from the store, through yonder's block
	var lo bytes.Buffer
	lk := agents.NewLinker(self, &lo)
	var lerr error
	if relink {
		lerr = lk.Link(nil)
	} else if len(hs) > 0 {
		lerr = lk.Link(hs)
	}
	if lerr == nil {
		for p, err := range lk.Blocked() {
			lerr = fmt.Errorf("%s keeps its old text: %w", agents.Tilde(p, home), err)
			break
		}
	}
	if lerr != nil {
		return putBack("linking the store in failed (" + lerr.Error() + ")")
	}
	data, _ := json.MarshalIndent(tr, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "trim.json"), data, 0o600); err != nil {
		return putBack("couldn't save the undo record: " + err.Error())
	}
	if len(hs) > 0 {
		d.Linked = "linked the store into " + strings.Join(hs, ", ")
	}
	return d
}

// noSymlinkOnWay refuses a path with a symlink anywhere below home, the file
// included: reconcile never writes through one, the same rule link follows.
func noSymlinkOnWay(home, p string) error {
	rel, err := filepath.Rel(home, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is outside the home folder", p)
	}
	cur := home
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink, and yonder never writes through one", agents.Tilde(cur, home))
		}
	}
	return nil
}

// ownHash is the hash of the user's own text: the file without yonder's block.
func ownHash(text string) string { return agents.OwnHash([]byte(text)) }

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && r != '-' {
			return false
		}
	}
	return true
}

// writeKeepMode writes through a temp file and a rename, with the file's mode.
func writeKeepMode(p string, data []byte, mode os.FileMode) error {
	tmp := p + ".yonder-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// undoHere puts this machine's trimmed files back, each only if it is still
// what the reconcile wrote. a file already back counts as done, so a retry
// finishes a partial undo.
func undoHere(id string) acceptDone {
	d := acceptDone{Accepted: []string{}, Skipped: []skip{}}
	if !validID(id) {
		d.Skipped = append(d.Skipped, skip{"", "bad reconcile id"})
		return d
	}
	data, err := os.ReadFile(filepath.Join(reconcileDir(), id, "trim.json"))
	if errors.Is(err, os.ErrNotExist) {
		return d // nothing was trimmed here
	}
	if err != nil {
		d.Skipped = append(d.Skipped, skip{"", err.Error()})
		return d
	}
	var tr trimRecord
	if err := json.Unmarshal(data, &tr); err != nil {
		d.Skipped = append(d.Skipped, skip{"", err.Error()})
		return d
	}
	home := agents.Home()
	for _, f := range tr.Files {
		p := agents.InstructionsPath(home, f.Path)
		if err := noSymlinkOnWay(home, p); err != nil {
			d.Skipped = append(d.Skipped, skip{f.Path, err.Error() + "; the backup is at " + agents.Tilde(f.Backup, home)})
			continue
		}
		cur, err := os.ReadFile(p)
		st, serr := os.Lstat(p)
		switch {
		case err != nil || serr != nil:
			d.Skipped = append(d.Skipped, skip{f.Path, "can't read it; the backup is at " + agents.Tilde(f.Backup, home)})
			continue
		case ownHash(string(cur)) == f.Before:
			d.Accepted = append(d.Accepted, f.Path) // already back
			continue
		case ownHash(string(cur)) != f.After:
			d.Skipped = append(d.Skipped, skip{f.Path, "it changed since the reconcile, so it is left as it is; the backup is at " + agents.Tilde(f.Backup, home)})
			continue
		}
		old, err := os.ReadFile(f.Backup)
		if err == nil {
			err = writeKeepMode(p, old, st.Mode().Perm())
		}
		if err != nil {
			d.Skipped = append(d.Skipped, skip{f.Path, err.Error()})
			continue
		}
		d.Accepted = append(d.Accepted, f.Path)
	}
	return d
}

func (a *app) reconcileUndo(id string) error {
	s, self, err := a.agentStore("agents reconcile --undo")
	if err != nil {
		return err
	}
	if id == "last" || id == "" {
		es, _ := os.ReadDir(reconcileDir())
		var ids []string
		for _, e := range es {
			if r, err := loadRecord(e.Name()); err == nil && !r.Undone {
				ids = append(ids, r.ID)
			}
		}
		if len(ids) == 0 {
			return errors.New("no reconcile to undo here; undo runs on the machine that applied it")
		}
		sort.Strings(ids)
		id = ids[len(ids)-1]
	}
	rec, err := loadRecord(id)
	if err != nil {
		return fmt.Errorf("no reconcile %s here (undo runs on the machine that applied it): %w", id, err)
	}
	if rec.Undone {
		return fmt.Errorf("reconcile %s was already undone", id)
	}
	failed := false
	for _, m := range rec.Machines {
		var d acceptDone
		if m == self {
			d = undoHere(id)
		} else {
			mr, err := a.store.Machine(m)
			if err == nil {
				var out, errb bytes.Buffer
				var code int
				code, err = a.execOn(mr, self, yonderCmd(mr, "_reconcile-undo", id), nil, &out, &errb)
				if err == nil && code != 0 {
					err = errors.New(firstLine(errb.String()))
				}
				if err == nil {
					err = json.Unmarshal(lastJSON(out.Bytes()), &d)
				}
			}
			if err != nil {
				fmt.Fprintf(a.out, "%s: not undone (%v); run this again when it is online\n", m, err)
				failed = true
				continue
			}
		}
		for _, p := range d.Accepted {
			fmt.Fprintf(a.out, "%s: put back ~/%s\n", m, p)
		}
		for _, sk := range d.Skipped {
			fmt.Fprintf(a.out, "%s: ~/%s: %s\n", m, sk.Path, agents.Printable(sk.Why))
			failed = true
		}
	}
	changed := 0
	if _, err := withStore(s, func() error {
		for _, rel := range sortedKeys(rec.Store) {
			e := rec.Store[rel]
			cur, ok := s.Live(rel)
			if (e.Before == nil && !ok) || (e.Before != nil && ok && string(cur) == *e.Before) {
				continue // already back
			}
			if !ok || string(cur) != e.After {
				fmt.Fprintf(a.out, "store %s changed since the reconcile; left as it is\n", rel)
				failed = true
				continue
			}
			if e.Before == nil {
				if err := os.Remove(filepath.Join(s.Dir, filepath.FromSlash(rel))); err != nil {
					return err
				}
			} else if err := s.Put(rel, []byte(*e.Before), false); err != nil {
				return err
			}
			changed++
			fmt.Fprintf(a.out, "store: put back %s\n", rel)
		}
		return nil
	}); err != nil {
		return err
	}
	if changed > 0 {
		a.syncStore(agents.StoreName, self, nil, syncOpts{quiet: true})
	}
	if !failed {
		rec.Undone = true
		if err := saveRecord(rec); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "reconcile %s undone\n", id)
		return nil
	}
	fmt.Fprintf(a.out, "reconcile %s is partly undone; fix what is named above, then run `yonder agents reconcile --undo %s` again\n", id, id)
	return quietExit(ExitError)
}

func loadRecord(id string) (reconcileRecord, error) {
	var r reconcileRecord
	if !validID(id) {
		return r, errors.New("bad id")
	}
	data, err := os.ReadFile(filepath.Join(reconcileDir(), id, "record.json"))
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(data, &r)
}

// _reconcile-trim: a trimRequest on stdin; --probe only says it is there
func newReconcileTrimCmd() *cobra.Command {
	var probe bool
	cmd := &cobra.Command{
		Use:    "_reconcile-trim",
		Hidden: true,
		Args:   usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			_, self, err := a.agentStore("agents reconcile")
			if err != nil {
				return err
			}
			if probe {
				fmt.Fprintln(a.out, `{"ok":true}`)
				return nil
			}
			var req trimRequest
			if err := json.NewDecoder(io.LimitReader(a.in, 4<<20)).Decode(&req); err != nil {
				return fmt.Errorf("bad request: %w", err)
			}
			data, _ := json.Marshal(a.trimHere(self, req))
			fmt.Fprintln(a.out, string(data))
			return nil
		},
	}
	cmd.Flags().BoolVar(&probe, "probe", false, "only check this yonder can trim")
	return cmd
}

func newReconcileUndoCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_reconcile-undo <id>",
		Hidden: true,
		Args:   usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, _ := json.Marshal(undoHere(args[0]))
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
}
