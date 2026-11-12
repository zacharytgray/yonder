package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Version is one replaced or deleted copy of a file, kept under state.
type Version struct {
	N     int       `json:"n"` // 1 is the most recent
	ID    string    `json:"id"`
	State FileState `json:"state"`
	Why   string    `json:"why"`
	data  string
}

type versionMeta struct {
	State FileState `json:"state"`
	Why   string    `json:"why"`
}

// keep saves data as a replaced version of rel, then prunes old ones.
func (s *Store) keep(rel string, data []byte, f FileState, why string) {
	dir := filepath.Join(s.historyDir(), filepath.FromSlash(rel))
	id := strconv.FormatInt(s.now().UnixNano(), 10)
	if err := writeAtomic(filepath.Join(dir, id+".data"), data, 0o600); err != nil {
		return
	}
	meta, _ := json.Marshal(versionMeta{State: f, Why: why})
	_ = writeAtomic(filepath.Join(dir, id+".json"), meta, 0o600)
	s.pruneHistory(rel)
}

// keepLive saves what is live at rel now, if anything.
func (s *Store) keepLive(rel, why string) {
	data, err := os.ReadFile(s.livePath(rel))
	if err != nil {
		return
	}
	f := s.st.Files[rel]
	f.Hash, f.Size, f.Deleted = hashOf(data), int64(len(data)), false
	if info, err := os.Stat(s.livePath(rel)); err == nil {
		f.Exec = info.Mode()&0o111 != 0
	}
	s.keep(rel, data, f, why)
}

// History lists the kept versions of rel, newest first.
func (s *Store) History(rel string) ([]Version, error) {
	if err := ValidRel(rel); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.historyDir(), filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Version
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".data")
		if !ok || e.IsDir() {
			continue
		}
		v := Version{ID: id, data: filepath.Join(dir, e.Name())}
		if b, err := os.ReadFile(filepath.Join(dir, id+".json")); err == nil {
			var m versionMeta
			if json.Unmarshal(b, &m) == nil {
				v.State, v.Why = m.State, m.Why
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return idNum(out[i].ID) > idNum(out[j].ID) })
	for i := range out {
		out[i].N = i + 1
	}
	return out, nil
}

// Data reads a kept version.
func (v Version) Data() ([]byte, error) { return os.ReadFile(v.data) }

func idNum(id string) int64 {
	n, _ := strconv.ParseInt(id, 10, 64)
	return n
}

func (s *Store) pruneHistory(rel string) {
	vs, err := s.History(rel)
	if err != nil {
		return
	}
	now := s.now()
	for _, v := range vs {
		age := now.Sub(timeOfID(v.ID))
		if v.N > HistoryKeep || age > HistoryTTL {
			os.Remove(v.data)
			os.Remove(strings.TrimSuffix(v.data, ".data") + ".json")
		}
	}
}

func timeOfID(id string) time.Time { return time.Unix(0, idNum(id)) }

// Restore puts back version n of rel (1 is the most recent kept). what is
// live now is kept first. the next scan records it as a change here.
func (s *Store) Restore(rel string, n int) (Version, error) {
	vs, err := s.History(rel)
	if err != nil {
		return Version{}, err
	}
	if len(vs) == 0 {
		return Version{}, fmt.Errorf("no kept versions of %s", rel)
	}
	if n < 1 || n > len(vs) {
		return Version{}, fmt.Errorf("%s has versions 1 to %d", rel, len(vs))
	}
	v := vs[n-1]
	data, err := v.Data()
	if err != nil {
		return v, err
	}
	if _, err := s.State(); err != nil {
		return v, err
	}
	s.keepLive(rel, "replaced by restore")
	if err := s.writeLive(rel, data, v.State.Exec); err != nil {
		return v, err
	}
	if _, ok := s.st.Pending[rel]; ok {
		s.dropPending(rel, "replaced by restore")
	}
	return v, nil
}
