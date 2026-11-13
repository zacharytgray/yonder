package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func (s *Store) livePath(rel string) string { return filepath.Join(s.Dir, filepath.FromSlash(rel)) }
func (s *Store) pendingPath(rel string) string {
	return filepath.Join(s.pendingDir(), filepath.FromSlash(rel))
}

func (s *Store) root() (*os.Root, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenRoot(s.Dir)
}

// writeLive writes rel inside the store folder, through an os.Root so no
// link in the store can send it elsewhere.
func (s *Store) writeLive(rel string, data []byte, exec bool) error {
	r, err := s.root()
	if err != nil {
		return err
	}
	defer r.Close()
	if d := path.Dir(rel); d != "." {
		if err := r.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	tmp := path.Join(path.Dir(rel), ".yonder-tmp-"+hex.EncodeToString(b[:]))
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	if err := r.WriteFile(tmp, data, mode); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	if err := r.Chmod(tmp, mode); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	if err := r.Rename(tmp, rel); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	return nil
}

// removeLive deletes rel and any folders it leaves empty.
func (s *Store) removeLive(rel string) error {
	r, err := s.root()
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for d := path.Dir(rel); d != "."; d = path.Dir(d) {
		if r.Remove(d) != nil { // not empty, or gone
			break
		}
	}
	return nil
}

// live is what is on disk at rel now.
func (s *Store) live(rel string) (data []byte, exec, exists bool) {
	info, err := os.Lstat(s.livePath(rel))
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, false
	}
	data, err = os.ReadFile(s.livePath(rel))
	if err != nil {
		return nil, false, false
	}
	return data, info.Mode()&0o111 != 0, true
}

// liveAsRecorded: the file on disk is what the state says is live here.
// false means it was edited after the last scan.
func (s *Store) liveAsRecorded(rel string) bool {
	data, exec, exists := s.live(rel)
	if p, ok := s.st.Pending[rel]; ok {
		return exists == p.LiveExists && (!exists || (hashOf(data) == p.LiveHash && exec == p.LiveExec))
	}
	cur, known := s.st.Files[rel]
	if !known || cur.Deleted {
		return !exists
	}
	return exists && hashOf(data) == cur.Hash && exec == cur.Exec
}

// content is this store's version of rel: the pending copy when one waits
// for review, else the live file. nil when it no longer matches the state.
func (s *Store) content(rel string) []byte {
	f, ok := s.st.Files[rel]
	if !ok || f.Deleted {
		return nil
	}
	var data []byte
	if _, pending := s.st.Pending[rel]; pending {
		data, _ = os.ReadFile(s.pendingPath(rel))
	} else {
		data, _, _ = s.live(rel)
	}
	if data == nil || hashOf(data) != f.Hash {
		return nil
	}
	return data
}

// dropPending discards a change that waited for review, keeping it in history.
func (s *Store) dropPending(rel, why string) {
	if data, err := os.ReadFile(s.pendingPath(rel)); err == nil {
		s.keep(rel, data, s.st.Files[rel], "waited for review, "+why)
	}
	os.Remove(s.pendingPath(rel))
	delete(s.st.Pending, rel)
}

// errors from apply that skip one path and let the rest go on
var (
	errNotNewer = errors.New("changed on this machine meanwhile; the next sync settles it")
	errEdited   = errors.New("edited here after the last scan; the next sync settles it")
)

// apply makes next the store's version of rel. data is the content (nil
// for a delete, or when the content here already matches). local says the
// content came from this machine's own store; anything else that needs
// review goes to pending/ instead of live. a version's origin is written by
// whoever sent it, so it never decides review.
func (s *Store) apply(rel string, next FileState, data []byte, from string, local bool) (string, error) {
	if err := ValidRel(rel); err != nil {
		return "", err
	}
	if other := s.caseTwin(rel); other != "" && !next.Deleted {
		return "", refusedError{Refusal{Reason: "its name differs from " + other + " only by case, which a Mac can't hold apart", Fix: "rename one of them", Final: true}}
	}
	cur, known := s.st.Files[rel]
	if known && !next.Version.Dominates(cur.Version) {
		return "", errNotNewer
	}
	if known && next.Version.Compare(cur.Version) == Equal && next.sameContent(cur) {
		return "", nil
	}
	if !s.liveAsRecorded(rel) {
		return "", errEdited
	}
	if next.Deleted {
		_, _, exists := s.live(rel)
		if !known && !exists {
			// a marker for a file never held here: keep it quietly
			s.st.Files[rel] = next
			s.st.Marked[rel] = s.now()
			return "", nil
		}
		if exists {
			s.keepLive(rel, "deleted by a change from "+orSelf(next.Origin.Machine))
			if err := s.removeLive(rel); err != nil {
				return "", err
			}
		}
		if _, ok := s.st.Pending[rel]; ok {
			s.dropPending(rel, "deleted by a later change")
		}
		s.st.Files[rel] = next
		s.st.Marked[rel] = s.now()
		s.logChange(rel, "delete", next, from)
		return "deleted", nil
	}
	if data == nil {
		data = s.content(rel)
		if data == nil || hashOf(data) != next.Hash {
			if d, _, ok := s.live(rel); ok && hashOf(d) == next.Hash {
				data = d
			}
		}
	}
	if data == nil || hashOf(data) != next.Hash {
		return "", fmt.Errorf("content for %s is missing or doesn't match its hash", rel)
	}
	if len(data) > MaxFile {
		return "", fmt.Errorf("%d bytes, over the %d MiB limit per file", len(data), MaxFile>>20)
	}
	if s.Kind.Check != nil {
		if r := s.Kind.Check(rel, data); r != nil && (!next.Allowed || nameOnly(r)) {
			return "", refusedError{*r}
		}
	}
	if files, total := s.totals(rel); files+1 > MaxFiles || total+int64(len(data)) > MaxTotal {
		return "", refusedError{Refusal{Reason: fmt.Sprintf("the store would be over its limits (%d files, %d MiB)", MaxFiles, MaxTotal>>20), Fix: "remove files from the store"}}
	}
	// an allowed file stays allowed here, so this machine's own scans take it too
	if next.Allowed {
		s.st.Allow[rel] = next.Hash
	} else {
		delete(s.st.Allow, rel)
	}
	liveData, liveExec, liveExists := s.live(rel)
	liveSame := liveExists && hashOf(liveData) == next.Hash && liveExec == next.Exec
	if !liveSame && !local && s.Kind.Review != nil && s.Kind.Review(rel, next.Exec) {
		if err := writeAtomic(s.pendingPath(rel), data, 0o600); err != nil {
			return "", err
		}
		if _, ok := s.st.Pending[rel]; !ok {
			p := Pending{LiveExists: liveExists, Since: s.now()}
			if liveExists {
				p.LiveHash, p.LiveExec = hashOf(liveData), liveExec
			}
			s.st.Pending[rel] = p
		}
		s.st.Files[rel] = next
		s.logChange(rel, "pending", next, from)
		return "pending", nil
	}
	if !liveSame {
		if liveExists {
			s.keepLive(rel, "replaced by a change from "+orSelf(next.Origin.Machine))
		}
		if err := s.writeLive(rel, data, next.Exec); err != nil {
			return "", err
		}
	}
	if _, ok := s.st.Pending[rel]; ok {
		os.Remove(s.pendingPath(rel))
		delete(s.st.Pending, rel)
	}
	s.st.Files[rel] = next
	delete(s.st.Marked, rel)
	s.logChange(rel, "edit", next, from)
	return "updated", nil
}

// caseTwin is another path held here that differs from rel only by case.
func (s *Store) caseTwin(rel string) string {
	for p, f := range s.st.Files {
		if !f.Deleted && p != rel && strings.EqualFold(p, rel) {
			return p
		}
	}
	return ""
}

// totals counts the files and bytes held here, leaving out rel.
func (s *Store) totals(rel string) (files int, bytes int64) {
	for p, f := range s.st.Files {
		if !f.Deleted && p != rel {
			files++
			bytes += f.Size
		}
	}
	return files, bytes
}

func orSelf(m string) string {
	if m == "" {
		return "this machine"
	}
	return m
}

type refusedError struct{ r Refusal }

func (e refusedError) Error() string { return "refused: " + e.r.Reason }

// PendingItem is one change waiting for review here.
type PendingItem struct {
	Path    string    `json:"path"`
	State   FileState `json:"state"`
	Pending Pending   `json:"pending"`
}

// PendingList lists the changes waiting for review, by path.
func (s *Store) PendingList() ([]PendingItem, error) {
	st, err := s.State()
	if err != nil {
		return nil, err
	}
	var out []PendingItem
	for rel, p := range st.Pending {
		out = append(out, PendingItem{Path: rel, State: st.Files[rel], Pending: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// PendingData is what is live and what waits, for showing a diff.
func (s *Store) PendingData(rel string) (live, incoming []byte, err error) {
	st, err := s.State()
	if err != nil {
		return nil, nil, err
	}
	if _, ok := st.Pending[rel]; !ok {
		return nil, nil, fmt.Errorf("nothing waits for review at %s", rel)
	}
	live, _, _ = s.live(rel)
	incoming, err = os.ReadFile(s.pendingPath(rel))
	return live, incoming, err
}

// Accept makes a waiting change live. callers hold the lock.
func (s *Store) Accept(rel string) error {
	st, err := s.State()
	if err != nil {
		return err
	}
	if _, ok := st.Pending[rel]; !ok {
		return fmt.Errorf("nothing waits for review at %s", rel)
	}
	if !s.liveAsRecorded(rel) {
		return fmt.Errorf("%s was edited here since; run `yonder sync` first", rel)
	}
	data, err := os.ReadFile(s.pendingPath(rel))
	if err != nil {
		return err
	}
	f := st.Files[rel]
	if hashOf(data) != f.Hash {
		return fmt.Errorf("the waiting copy of %s doesn't match its hash; run `yonder sync` to fetch it again", rel)
	}
	if _, _, exists := s.live(rel); exists {
		s.keepLive(rel, "replaced by accept")
	}
	if err := s.writeLive(rel, data, f.Exec); err != nil {
		return err
	}
	os.Remove(s.pendingPath(rel))
	delete(st.Pending, rel)
	s.logChange(rel, "accept", f, "")
	return nil
}

// Allow waives the secret check for rel's current content.
func (s *Store) Allow(rel string) error {
	st, err := s.State()
	if err != nil {
		return err
	}
	data, _, exists := s.live(rel)
	if !exists {
		return fmt.Errorf("no file %s in the store", rel)
	}
	if s.Kind.Check != nil {
		r := s.Kind.Check(rel, data)
		if r == nil {
			return fmt.Errorf("%s passes the checks already", rel)
		}
		if nameOnly(r) {
			return fmt.Errorf("%s: %s; a name like that never syncs", rel, r.Reason)
		}
	}
	st.Allow[rel] = hashOf(data)
	return nil
}

// Put writes a file into the store here, keeping what it replaces. callers
// hold the lock, then scan to record it.
func (s *Store) Put(rel string, data []byte, exec bool) error {
	if err := ValidRel(rel); err != nil {
		return err
	}
	if _, err := s.State(); err != nil {
		return err
	}
	if _, _, exists := s.live(rel); exists {
		s.keepLive(rel, "replaced here")
	}
	return s.writeLive(rel, data, exec)
}

// Refusals lists the files here that can't sync, without changing anything.
func (s *Store) Refusals() ([]Refusal, error) {
	if _, err := s.State(); err != nil {
		return nil, err
	}
	_, refused, err := s.readDisk()
	return refused, err
}

// Live reads a store file as it is on disk here.
func (s *Store) Live(rel string) ([]byte, bool) {
	data, _, ok := s.live(rel)
	return data, ok
}
