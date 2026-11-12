package store

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ConflictName is where the losing copy of a concurrent edit goes:
// <name>.conflict-<machine>-<yyyymmdd>.<ext>, beside the file.
func ConflictName(rel, machine string, t time.Time, n int) string {
	dir, base := path.Split(rel)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" { // a dotfile: the whole name is the stem
		stem, ext = base, ""
	}
	tag := ".conflict-" + machine + "-" + t.UTC().Format("20060102")
	if n > 1 {
		tag += fmt.Sprintf("-%d", n)
	}
	return dir + stem + tag + ext
}

var conflictTag = regexp.MustCompile(`\.conflict-([a-z0-9]([a-z0-9-]*[a-z0-9])?)-(\d{8})(-\d+)?`)

// ConflictOf reports whether rel is a conflict copy, and of which path and machine.
func ConflictOf(rel string) (base, machine string, ok bool) {
	dir, name := path.Split(rel)
	loc := conflictTag.FindStringSubmatchIndex(name)
	if loc == nil {
		return "", "", false
	}
	return dir + name[:loc[0]] + name[loc[1]:], name[loc[2]:loc[3]], true
}

// Conflicts maps each path to its conflict copies held here.
func (s *Store) Conflicts() (map[string][]string, error) {
	st, err := s.State()
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for rel, f := range st.Files {
		if f.Deleted {
			continue
		}
		if base, _, ok := ConflictOf(rel); ok {
			out[base] = append(out[base], rel)
		}
	}
	for _, cs := range out {
		sort.Strings(cs)
	}
	return out, nil
}

// Resolve settles a conflict at rel: keep names the machine whose version
// wins ("mine" is this one; "theirs" is the only other one). the winner goes
// at rel and every conflict copy is deleted. callers hold the lock, then
// scan and sync.
func (s *Store) Resolve(rel, keep string) (kept string, removed []string, err error) {
	if err := ValidRel(rel); err != nil {
		return "", nil, err
	}
	if b, _, ok := ConflictOf(rel); ok {
		rel = b
	}
	all, err := s.Conflicts()
	if err != nil {
		return "", nil, err
	}
	copies := all[rel]
	if len(copies) == 0 {
		return "", nil, fmt.Errorf("no conflict copies of %s", rel)
	}
	type cand struct{ path, machine string }
	cands := []cand{{rel, s.st.Files[rel].Origin.Machine}}
	for _, c := range copies {
		_, m, _ := ConflictOf(c)
		cands = append(cands, cand{c, m})
	}
	var pick *cand
	switch keep {
	case "mine":
		keep = s.Self
	case "theirs":
		var others []cand
		for _, c := range cands {
			if c.machine != s.Self {
				others = append(others, c)
			}
		}
		if len(others) != 1 {
			return "", nil, fmt.Errorf("%d versions of %s are from other machines; name one: --keep <machine>", len(others), rel)
		}
		pick = &others[0]
	}
	if pick == nil {
		for i := range cands {
			if cands[i].machine == keep {
				pick = &cands[i]
				break
			}
		}
	}
	if pick == nil {
		var names []string
		for _, c := range cands {
			names = append(names, c.machine+" ("+c.path+")")
		}
		return "", nil, fmt.Errorf("no version of %s from %s; versions: %s", rel, keep, strings.Join(names, ", "))
	}
	if pick.path != rel {
		data, exec, ok := s.live(pick.path)
		if !ok {
			return "", nil, fmt.Errorf("%s is not in the store folder", pick.path)
		}
		s.keepLive(rel, "replaced by resolve")
		if err := s.writeLive(rel, data, exec); err != nil {
			return "", nil, err
		}
	}
	for _, c := range copies {
		s.keepLive(c, "removed by resolve")
		if err := s.removeLive(c); err != nil {
			return "", removed, err
		}
		removed = append(removed, c)
	}
	return pick.machine, removed, nil
}
