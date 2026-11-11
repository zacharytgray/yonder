package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

var ErrNotFound = errors.New("machine not found")

// Store reads and writes the registry under one config dir.
type Store struct {
	Dir string
}

func Open() *Store { return &Store{Dir: ConfigDir()} }

func (s *Store) machinesDir() string { return filepath.Join(s.Dir, "machines") }
func (s *Store) removedDir() string  { return filepath.Join(s.Dir, "removed") }
func (s *Store) configPath() string  { return filepath.Join(s.Dir, "config.toml") }

func (s *Store) Machine(name string) (Machine, error) {
	if err := ValidName(name); err != nil {
		return Machine{}, err
	}
	var m Machine
	err := readTOML(filepath.Join(s.machinesDir(), name+".toml"), &m)
	if errors.Is(err, os.ErrNotExist) {
		return Machine{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return m, err
}

func (s *Store) Machines() ([]Machine, error) {
	var out []Machine
	err := eachTOML(s.machinesDir(), func(path string) error {
		var m Machine
		if err := readTOML(path, &m); err != nil {
			return err
		}
		out = append(out, m)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

func (s *Store) Tombstones() ([]Tombstone, error) {
	var out []Tombstone
	err := eachTOML(s.removedDir(), func(path string) error {
		var t Tombstone
		if err := readTOML(path, &t); err != nil {
			return err
		}
		out = append(out, t)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

func (s *Store) Tombstone(name string) (Tombstone, bool) {
	var t Tombstone
	if ValidName(name) != nil {
		return t, false
	}
	err := readTOML(filepath.Join(s.removedDir(), name+".toml"), &t)
	return t, err == nil
}

func (s *Store) Bundle() (Bundle, error) {
	ms, err := s.Machines()
	if err != nil {
		return Bundle{}, err
	}
	ts, err := s.Tombstones()
	if err != nil {
		return Bundle{}, err
	}
	return Merge(Bundle{Machines: ms, Tombstones: ts}), nil
}

func (s *Store) SaveMachine(m Machine) error {
	if err := ValidName(m.Name); err != nil {
		return err
	}
	return writeTOML(filepath.Join(s.machinesDir(), m.Name+".toml"), m)
}

func (s *Store) SaveTombstone(t Tombstone) error {
	if err := ValidName(t.Name); err != nil {
		return err
	}
	return writeTOML(filepath.Join(s.removedDir(), t.Name+".toml"), t)
}

// Apply makes the disk hold exactly b. callers pass a merged bundle.
func (s *Store) Apply(b Bundle) error {
	keepM := map[string]bool{}
	for _, m := range b.Machines {
		if err := s.SaveMachine(m); err != nil {
			return err
		}
		keepM[m.Name+".toml"] = true
	}
	keepT := map[string]bool{}
	for _, t := range b.Tombstones {
		if err := s.SaveTombstone(t); err != nil {
			return err
		}
		keepT[t.Name+".toml"] = true
	}
	if err := prune(s.machinesDir(), keepM); err != nil {
		return err
	}
	return prune(s.removedDir(), keepT)
}

// MergeIn merges incoming into the local registry and returns the result.
func (s *Store) MergeIn(incoming Bundle) (Bundle, error) {
	local, err := s.Bundle()
	if err != nil {
		return Bundle{}, err
	}
	return s.save(local, Merge(local, incoming))
}

// MergeFrom merges a bundle that peer sender sent, under FromPeer's rules.
// clear_lost is honoured unless it names the sender itself.
func (s *Store) MergeFrom(incoming Bundle, sender string) (Bundle, error) {
	cfg, err := s.Config()
	if err != nil {
		return Bundle{}, err
	}
	local, err := s.Bundle()
	if err != nil {
		return Bundle{}, err
	}
	base := local
	for _, name := range incoming.ClearLost {
		if name != sender {
			base.Tombstones = slices.DeleteFunc(slices.Clone(base.Tombstones), func(t Tombstone) bool { return t.Name == name && t.Lost })
		}
	}
	return s.save(local, Merge(base, FromPeer(local, incoming, cfg.Self, time.Now())))
}

// Remove writes t and drops the record of that name here, whatever its added_at.
func (s *Store) Remove(t Tombstone) error {
	local, err := s.Bundle()
	if err != nil {
		return err
	}
	base := Bundle{Machines: slices.DeleteFunc(slices.Clone(local.Machines), func(m Machine) bool { return m.Name == t.Name }), Tombstones: local.Tombstones}
	_, err = s.save(local, Merge(base, Bundle{Tombstones: []Tombstone{t}}))
	return err
}

// ClearLost deletes a lost tombstone for name, so a re-add can take its place.
func (s *Store) ClearLost(name string) error {
	if t, ok := s.Tombstone(name); !ok || !t.Lost {
		return nil
	}
	return os.Remove(filepath.Join(s.removedDir(), name+".toml"))
}

// save writes merged. each tombstone keeps the client keys of the record (or
// tombstone) this machine held for that name, so lost-key cleanup only ever
// strips keys this machine itself had on record.
func (s *Store) save(local, merged Bundle) (Bundle, error) {
	held := map[string][]string{}
	for _, t := range local.Tombstones {
		held[t.Name] = t.HeldKeys
	}
	for _, m := range local.Machines {
		held[m.Name] = append(slices.Clone(held[m.Name]), m.SSHClientKeys...)
	}
	for i, t := range merged.Tombstones {
		merged.Tombstones[i].HeldKeys = held[t.Name]
	}
	return merged, s.Apply(merged)
}

func prune(dir string, keep map[string]bool) error {
	return eachTOML(dir, func(path string) error {
		if keep[filepath.Base(path)] {
			return nil
		}
		return os.Remove(path)
	})
}

func eachTOML(dir string, fn func(string) error) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if err := fn(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func readTOML(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeTOML(path string, v any) error {
	data, err := toml.Marshal(v)
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// writeFile writes atomically via a temp file and rename.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
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
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
