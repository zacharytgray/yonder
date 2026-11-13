package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ValidRel checks a store path: relative, slash-separated, clean, no "..".
func ValidRel(rel string) error {
	switch {
	case rel == "" || rel == ".":
		return errors.New("empty path")
	case strings.ContainsRune(rel, 0) || strings.Contains(rel, "\\"):
		return fmt.Errorf("bad path %q", rel)
	case path.IsAbs(rel) || path.Clean(rel) != rel:
		return fmt.Errorf("path %q is not a clean relative path", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("path %q has an empty, . or .. part", rel)
		}
	}
	return nil
}

// tempName is a yonder temp file, never synced
func tempName(base string) bool { return strings.HasPrefix(base, ".yonder-") }

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type disk struct {
	hash  string
	exec  bool
	size  int64
	mtime time.Time
}

// readDisk walks the store folder. files that can't sync come back as refusals.
// a missing store folder (errNoRoot) is not the same as an empty one.
func (s *Store) readDisk() (map[string]disk, []Refusal, error) {
	out := map[string]disk{}
	var refused []Refusal
	var total int64
	// a store folder moved and linked back (into a dotfiles repo, say) is read through the link
	root, err := filepath.EvalSymlinks(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil, errNoRoot
	}
	if err != nil {
		return nil, nil, err
	}
	folded := map[string]string{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
		base := d.Name()
		if tempName(base) || (s.Kind.Ignore != nil && s.Kind.Ignore(rel, d.IsDir())) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if err := ValidRel(rel); err != nil {
			refused = append(refused, Refusal{Path: rel, Reason: err.Error(), Fix: "rename it"})
			return nil
		}
		if other, ok := folded[strings.ToLower(rel)]; ok {
			refused = append(refused, Refusal{Path: rel, Reason: "its name differs from " + other + " only by case, which a Mac can't hold apart", Fix: "rename one of them", Final: true})
			return nil
		}
		folded[strings.ToLower(rel)] = rel
		if d.Type()&fs.ModeSymlink != 0 {
			refused = append(refused, Refusal{Path: rel, Reason: "a symlink; only regular files sync", Fix: "replace it with the file itself"})
			return nil
		}
		if !d.Type().IsRegular() {
			refused = append(refused, Refusal{Path: rel, Reason: "not a regular file", Fix: "remove it from the store"})
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > MaxFile {
			refused = append(refused, Refusal{Path: rel, Reason: fmt.Sprintf("%d bytes, over the %d MiB limit per file", info.Size(), MaxFile>>20), Fix: "keep large files out of the store"})
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if s.Kind.Check != nil {
			if r := s.Kind.Check(rel, data); r != nil {
				st, _ := s.State()
				if st == nil || st.Allow[rel] != hashOf(data) || nameOnly(r) {
					r.Path = rel
					refused = append(refused, *r)
					return nil
				}
			}
		}
		if len(out) >= MaxFiles {
			refused = append(refused, Refusal{Path: rel, Reason: fmt.Sprintf("the store is over its %d file limit", MaxFiles), Fix: "remove files from the store"})
			return nil
		}
		if total+info.Size() > MaxTotal {
			refused = append(refused, Refusal{Path: rel, Reason: fmt.Sprintf("the store is over its %d MiB limit", MaxTotal>>20), Fix: "remove large files from the store"})
			return nil
		}
		total += info.Size()
		out[rel] = disk{hash: hashOf(data), exec: info.Mode()&0o111 != 0, size: info.Size(), mtime: info.ModTime()}
		return nil
	})
	sort.Slice(refused, func(i, j int) bool { return refused[i].Path < refused[j].Path })
	return out, refused, err
}

// a refusal by name can't be waived; only a content match can
func nameOnly(r *Refusal) bool { return r.Final || strings.HasPrefix(r.Reason, "the name ") }

// errNoRoot: the store folder is gone
var errNoRoot = errors.New("no store folder")

// ErrMassDelete: a scan would delete most of the store. that is more often a
// moved or broken folder than a choice, so it stops until asked.
var ErrMassDelete = errors.New("mass delete")

// massDelete is the guard: two or more deletes that are more than half of
// what is tracked. one file gone from a tiny store is just a delete.
func massDelete(deletes, tracked int) bool {
	return deletes >= 2 && deletes*2 > tracked
}

// countDeletes is how many tracked files a scan of files would delete.
func (s *Store) countDeletes(files map[string]disk, refused []Refusal) (deletes, tracked int) {
	refusedSet := map[string]bool{}
	for _, r := range refused {
		refusedSet[r.Path] = true
	}
	for rel, cur := range s.st.Files {
		if cur.Deleted {
			continue
		}
		if p, ok := s.st.Pending[rel]; ok && !p.LiveExists {
			continue
		}
		tracked++
		if _, ok := files[rel]; !ok && !refusedSet[rel] {
			deletes++
		}
	}
	return deletes, tracked
}

// Held reports why the next scan would stop, or "": a missing store folder
// or a mass delete. it changes nothing.
func (s *Store) Held() (string, error) {
	if _, err := s.State(); err != nil {
		return "", err
	}
	files, refused, err := s.readDisk()
	if err != nil && !errors.Is(err, errNoRoot) {
		return "", err
	}
	deletes, tracked := s.countDeletes(files, refused)
	switch {
	case errors.Is(err, errNoRoot) && tracked > 0:
		return fmt.Sprintf("the store folder %s is missing, but %d files were in it", s.Dir, tracked), nil
	case massDelete(deletes, tracked):
		return fmt.Sprintf("%d of the %d files in the store are gone from %s", deletes, tracked, s.Dir), nil
	}
	return "", nil
}

// Scan finds changes made here since the last scan and bumps this machine's
// counter for each. callers hold the lock. it returns the paths it changed
// and the files that can't sync.
func (s *Store) Scan() (changed []string, refused []Refusal, err error) {
	st, err := s.State()
	if err != nil {
		return nil, nil, err
	}
	files, refused, err := s.readDisk()
	if err != nil && !errors.Is(err, errNoRoot) {
		return nil, refused, err
	}
	refusedSet := map[string]bool{}
	for _, r := range refused {
		refusedSet[r.Path] = true
	}
	// count deletes before changing anything, so the guard can stop the scan whole
	deletes, tracked := s.countDeletes(files, refused)
	if errors.Is(err, errNoRoot) && tracked > 0 && !s.AllowDeletes {
		return nil, nil, fmt.Errorf("the %s store folder %s is missing, but %d files were in it; put it back, or delete them on purpose with `yonder sync --allow-deletes`: %w", s.Kind.Name, s.Dir, tracked, ErrMassDelete)
	}
	if !s.AllowDeletes && massDelete(deletes, tracked) {
		return nil, nil, fmt.Errorf("this scan would delete %d of the %d files in the %s store (%s); if the folder moved or broke, put it back; if you meant it, run `yonder sync --allow-deletes`: %w", deletes, tracked, s.Kind.Name, s.Dir, ErrMassDelete)
	}
	now := s.now()
	for rel, d := range files {
		cur, known := st.Files[rel]
		if p, ok := st.Pending[rel]; ok {
			if p.LiveExists && p.LiveHash == d.hash && p.LiveExec == d.exec {
				continue
			}
			// edited here while a change waited for review: the edit wins
			s.dropPending(rel, "replaced by an edit here")
		} else if known && !cur.Deleted && cur.Hash == d.hash && cur.Exec == d.exec {
			continue
		}
		t := d.mtime.UTC().Truncate(time.Second)
		if t.After(now.Add(5*time.Minute)) || t.IsZero() {
			t = now
		}
		next := FileState{
			Hash: d.hash, Exec: d.exec, Size: d.size, Version: cur.Version.Bump(s.Self),
			Origin: Origin{Machine: s.Self, Harness: s.Harness, Time: t},
		}
		next.Allowed = st.Allow[rel] == d.hash
		delete(st.Marked, rel)
		st.Files[rel] = next
		changed = append(changed, rel)
		s.logChange(rel, "edit", next, "")
	}
	for rel, cur := range st.Files {
		if cur.Deleted {
			if markedAt := laterOf(cur.Origin.Time, st.Marked[rel]); now.Sub(markedAt) > DeleteTTL {
				delete(st.Files, rel)
				delete(st.Marked, rel)
			}
			continue
		}
		if _, ok := files[rel]; ok || refusedSet[rel] {
			continue
		}
		if p, ok := st.Pending[rel]; ok {
			if !p.LiveExists {
				continue // a new file waiting for review; nothing live yet
			}
			s.dropPending(rel, "deleted here")
		}
		next := FileState{Version: cur.Version.Bump(s.Self), Deleted: true, Origin: Origin{Machine: s.Self, Harness: s.Harness, Time: now}}
		st.Marked[rel] = now
		st.Files[rel] = next
		changed = append(changed, rel)
		s.logChange(rel, "delete", next, "")
	}
	for rel := range st.Allow {
		if f, ok := st.Files[rel]; !ok || f.Deleted {
			delete(st.Allow, rel)
		}
	}
	var keep []Notice
	for _, n := range st.Notices {
		if now.Sub(n.Time) < DeleteTTL {
			keep = append(keep, n)
		}
	}
	st.Notices = keep
	sort.Strings(changed)
	return changed, refused, nil
}

// Manifest is what a machine says it holds.
type Manifest struct {
	V       int                  `json:"v"`
	Store   string               `json:"store"`
	Machine string               `json:"machine"`
	Files   map[string]FileState `json:"files"`
	Refused []Refusal            `json:"refused,omitempty"`
	// when this machine last synced with the caller, as it recorded it, and
	// with any other machine
	LastWith time.Time `json:"last_with,omitzero"`
	LastAny  time.Time `json:"last_any,omitzero"`
}

// manifest is what this machine holds, with its own record of its last
// sync with the caller.
func (s *Store) manifest(refused []Refusal, caller string) Manifest {
	m := Manifest{V: ProtocolVersion, Store: s.Kind.Name, Machine: s.Self, Files: s.st.Files, Refused: refused}
	if caller == "" {
		return m
	}
	m.LastWith = s.st.Peers[caller]
	m.LastAny = s.lastAny(caller)
	if held := s.st.Held[caller]; len(held) > 0 {
		// flag what this machine holds back from the caller; it skips them
		m.Files = map[string]FileState{}
		for p, f := range s.st.Files {
			if t, ok := held[p]; ok && !f.Deleted && !f.Origin.Time.After(t) {
				f.Held = true
			}
			m.Files[p] = f
		}
	}
	return m
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// LogEntry is one change this machine recorded: its own, or one it received.
type LogEntry struct {
	Time    time.Time `json:"time"` // when this machine recorded it
	Path    string    `json:"path"`
	Action  string    `json:"action"` // edit | delete | pending | accept | conflict | restore | resolve
	Origin  Origin    `json:"origin"`
	From    string    `json:"from,omitempty"` // the machine it arrived from
	Hash    string    `json:"hash,omitempty"`
	Version Vector    `json:"version"`
}

func (s *Store) logChange(rel, action string, f FileState, from string) {
	s.logs = append(s.logs, LogEntry{Time: s.now(), Path: rel, Action: action, Origin: f.Origin, From: from, Hash: f.Hash, Version: f.Version})
}

// flushLog appends the changes recorded since the last save.
func (s *Store) flushLog() error {
	if len(s.logs) == 0 {
		return nil
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return err
	}
	fh, err := os.OpenFile(s.logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	for _, e := range s.logs {
		data, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := fh.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	s.logs = nil
	return nil
}

// Log reads the change log, oldest first. rel narrows it to one path.
func (s *Store) Log(rel string) ([]LogEntry, error) {
	f, err := os.Open(s.logPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e LogEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if rel == "" || e.Path == rel {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}
