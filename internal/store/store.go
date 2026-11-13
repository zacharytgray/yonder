// Package store syncs a folder of files between fleet machines, peer to peer
// over the same ssh transport as everything else. each file carries a version
// counter per machine; a change one side has seen all of wins, two changes
// neither has seen are both kept. there is no daemon: a sync runs when a
// command asks for one.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
)

// limits on what a store syncs
const (
	MaxFile  = 1 << 20  // bytes per file
	MaxTotal = 50 << 20 // bytes per store
	// delete markers and notices older than this are dropped
	DeleteTTL = 90 * 24 * time.Hour
	// history keeps this many replaced versions per file, each at most HistoryTTL old
	HistoryKeep = 10
	HistoryTTL  = 30 * 24 * time.Hour
	// a remote handler waits this long for the store lock before saying busy
	PeerLockWait = 5 * time.Second
)

// MaxFiles is the file count limit per store. a var for tests.
var MaxFiles = 10000

// ProtocolVersion is the manifest version this yonder speaks.
const ProtocolVersion = 1

// Kind is one sort of store and its rules. stores register themselves.
type Kind struct {
	Name  string
	Layer string // the layer that must be on for it to sync
	// Ignore: paths that are never synced and never reported (a .git folder)
	Ignore func(rel string, dir bool) bool
	// Check: a reason this file can't sync, or nil. runs on both sides.
	Check func(rel string, data []byte) *Refusal
	// Review: an incoming change to this path waits for `accept` here
	Review func(rel string, exec bool) bool
	// AfterChange runs after a sync or command changed files here
	AfterChange func(s *Store, changed []string)
}

var kinds = map[string]Kind{}

// Register adds a store kind. call it from an init.
func Register(k Kind) { kinds[k.Name] = k }

// Lookup finds a registered store kind.
func Lookup(name string) (Kind, bool) {
	k, ok := kinds[name]
	return k, ok
}

// Names lists the registered stores.
func Names() []string {
	var out []string
	for n := range kinds {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Refusal is a file a store won't sync, with why and the fix.
type Refusal struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Fix    string `json:"fix,omitempty"`
	Final  bool   `json:"final,omitempty"` // no allow waives it
}

func (r Refusal) String() string {
	s := r.Path + ": " + r.Reason
	if r.Fix != "" {
		s += "; " + r.Fix
	}
	return s
}

// Origin is where a version came from.
type Origin struct {
	Machine string    `json:"machine"`
	Harness string    `json:"harness,omitempty"` // when yonder knows it
	Time    time.Time `json:"time"`
}

// FileState is one path in a manifest. a deleted path keeps its counters.
type FileState struct {
	Hash    string `json:"hash,omitempty"` // sha256 of the content; empty when deleted
	Exec    bool   `json:"exec,omitempty"`
	Size    int64  `json:"size"`
	Version Vector `json:"version"`
	Deleted bool   `json:"deleted,omitempty"`
	Origin  Origin `json:"origin"`
	Allowed bool   `json:"allowed,omitempty"` // the secret check was waived for this content
	Held    bool   `json:"held,omitempty"`    // in an offer: held back from the caller (see State.Held)
}

func (f FileState) sameContent(o FileState) bool {
	if f.Deleted || o.Deleted {
		return f.Deleted == o.Deleted
	}
	return f.Hash == o.Hash && f.Exec == o.Exec
}

// Pending is an incoming change that waits for review. the store's version
// of the path lives in pending/; the live file is what was there before.
type Pending struct {
	LiveHash   string    `json:"live_hash,omitempty"`
	LiveExec   bool      `json:"live_exec,omitempty"`
	LiveExists bool      `json:"live_exists"`
	Since      time.Time `json:"since"`
}

// Notice is something doctor reports for a while: a conflict, or a delete that lost to an edit.
type Notice struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"` // conflict | delete-lost
	Path   string    `json:"path"`
	Detail string    `json:"detail"`
}

// State is <state>/sync/<store>.json: everything sync knows about the files here.
type State struct {
	Files   map[string]FileState `json:"files"`
	Pending map[string]Pending   `json:"pending,omitempty"`
	Allow   map[string]string    `json:"allow,omitempty"` // path -> the hash whose secret check is waived
	Peers   map[string]time.Time `json:"peers,omitempty"` // last good sync with each machine
	Notices []Notice             `json:"notices,omitempty"`
	// files held back: path -> since when. set when a pair that hadn't synced
	// within DeleteTTL meets and only this side has the file. such a file may
	// be one the fleet deleted and forgot, so it is held from every machine
	// that doesn't know the path, until it changes here, a machine turns out
	// to know it, or --stale-ok
	Held map[string]time.Time `json:"held_files,omitempty"`
}

// Store is one synced folder on this machine.
type Store struct {
	Kind     Kind
	Dir      string // the folder itself
	StateDir string // <state>/sync
	Self     string
	Harness  string // recorded on changes this run makes; empty for edits found by a scan
	Now      func() time.Time
	// AllowDeletes lets a scan delete most of the store (see massDelete)
	AllowDeletes bool
	// StaleOK syncs files only one side knows even with a peer this machine
	// hasn't synced with for longer than DeleteTTL
	StaleOK bool

	st   *State
	lock *os.File
	logs []LogEntry // written with the state
}

// Open is the store of that kind for this machine, at the standard paths.
func Open(name, self string) (*Store, error) {
	k, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("no store named %s (stores: %s)", name, strings.Join(Names(), ", "))
	}
	return &Store{
		Kind: k, Dir: filepath.Join(registry.DataDir(), name),
		StateDir: filepath.Join(registry.StateDir(), "sync"), Self: self, Now: time.Now,
	}, nil
}

func (s *Store) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *Store) statePath() string  { return filepath.Join(s.StateDir, s.Kind.Name+".json") }
func (s *Store) lockPath() string   { return filepath.Join(s.StateDir, s.Kind.Name+".lock") }
func (s *Store) logPath() string    { return filepath.Join(s.StateDir, s.Kind.Name+".log.jsonl") }
func (s *Store) historyDir() string { return filepath.Join(s.StateDir, "history", s.Kind.Name) }
func (s *Store) pendingDir() string { return filepath.Join(s.StateDir, "pending", s.Kind.Name) }

// ErrBusy means another yonder holds the store's lock.
var ErrBusy = errors.New("busy: another yonder is syncing this store")

// Lock takes the store's lock, waiting up to wait. everything that reads
// and then writes the store's state holds it.
func (s *Store) Lock(wait time.Duration) error {
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			s.lock = f
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return ErrBusy
			}
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Unlock releases the lock and forgets the cached state.
func (s *Store) Unlock() {
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		s.lock.Close()
		s.lock = nil
	}
	s.st, s.logs = nil, nil
}

// State loads the state file once per lock.
func (s *Store) State() (*State, error) {
	if s.st != nil {
		return s.st, nil
	}
	st := &State{}
	data, err := os.ReadFile(s.statePath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, st); err != nil {
			return nil, fmt.Errorf("%s: %w", s.statePath(), err)
		}
	}
	if st.Files == nil {
		st.Files = map[string]FileState{}
	}
	if st.Pending == nil {
		st.Pending = map[string]Pending{}
	}
	if st.Allow == nil {
		st.Allow = map[string]string{}
	}
	if st.Peers == nil {
		st.Peers = map[string]time.Time{}
	}
	if st.Held == nil {
		st.Held = map[string]time.Time{}
	}
	if s.StaleOK {
		clear(st.Held)
	}
	s.st = st
	return st, nil
}

// Save writes the state file.
func (s *Store) Save() error {
	if s.st == nil {
		return nil
	}
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(s.statePath(), data, 0o600); err != nil {
		return err
	}
	return s.flushLog()
}

func (s *Store) notice(kind, path, detail string) {
	st := s.st
	st.Notices = append(st.Notices, Notice{Time: s.now(), Kind: kind, Path: path, Detail: detail})
	if n := len(st.Notices); n > 50 {
		st.Notices = st.Notices[n-50:]
	}
}

// writeAtomic writes via a temp file in the same folder and a rename.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".yonder-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
