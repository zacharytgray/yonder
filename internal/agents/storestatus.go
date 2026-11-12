package agents

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/store"
)

// StoreStatus is the agents store on this machine, in `agents status --json`.
type StoreStatus struct {
	LayerOn   bool                 `json:"layer_on"`
	Dir       string               `json:"dir"`
	Files     int                  `json:"files"`
	Bytes     int64                `json:"bytes"`
	Review    string               `json:"review"`  // [sync] review here
	Linked    []string             `json:"linked"`  // harnesses link wired in
	Paused    bool                 `json:"paused"`  // unlinked because the layer is off
	Pending   []string             `json:"pending"` // incoming changes waiting for accept
	Conflicts []string             `json:"conflicts"`
	Refused   []store.Refusal      `json:"refused"`
	LastSync  map[string]time.Time `json:"last_sync"`
	Notices   []store.Notice       `json:"notices"`    // from the last 7 days
	HomePaths []string             `json:"home_paths"` // instruction files that name an absolute home path
	Problems  []string             `json:"link_problems"`
	Large     []string             `json:"large_instructions"` // rendered instructions over 25 KB
	HeldBack  []string             `json:"held_back"`          // kept from others after a long absence
	Missing   bool                 `json:"missing"`            // the store folder is gone, with files on record
}

// StoreReport reads the store's state without changing anything.
func StoreReport(self string) (StoreStatus, error) {
	r := StoreStatus{
		LayerOn: registry.Open().LayerOn(registry.LayerAgents), Review: ReviewMode(),
		Linked: []string{}, Pending: []string{}, Conflicts: []string{}, Refused: []store.Refusal{},
		LastSync: map[string]time.Time{}, Notices: []store.Notice{}, HomePaths: []string{}, Problems: []string{}, Large: []string{}, HeldBack: []string{},
	}
	s, err := store.Open(StoreName, self)
	if err != nil {
		return r, err
	}
	r.Dir = s.Dir
	st, err := s.State()
	if err != nil {
		return r, err
	}
	for rel, f := range st.Files {
		if f.Deleted {
			continue
		}
		r.Files++
		r.Bytes += f.Size
		if isInstructions(rel) {
			if data, ok := s.Live(rel); ok {
				if lines := HomePaths(data); len(lines) > 0 {
					r.HomePaths = append(r.HomePaths, fmt.Sprintf("%s (line %s)", rel, joinInts(lines)))
				}
			}
		}
	}
	for p := range st.Pending {
		r.Pending = append(r.Pending, p)
	}
	for p := range st.Suspect {
		r.HeldBack = append(r.HeldBack, p)
	}
	sort.Strings(r.HeldBack)
	if _, err := os.Stat(s.Dir); err != nil && r.Files > 0 {
		r.Missing = true
	}
	cs, _ := s.Conflicts()
	for base := range cs {
		r.Conflicts = append(r.Conflicts, base)
	}
	for m, t := range st.Peers {
		r.LastSync[m] = t
	}
	for _, n := range st.Notices {
		if n.Kind == "delete-lost" && time.Since(n.Time) < 7*24*time.Hour {
			r.Notices = append(r.Notices, n)
		}
	}
	if ref, err := s.Refusals(); err == nil {
		r.Refused = ref
	}
	l := NewLinker(self, nil)
	if ls, err := l.State(); err == nil {
		r.Paused = ls.Paused
		if !ls.Paused {
			r.Linked = append(r.Linked, ls.Harnesses...)
		}
	}
	r.Problems = append(r.Problems, l.Problems()...)
	r.Large = append(r.Large, l.LargeTexts()...)
	sort.Strings(r.Pending)
	sort.Strings(r.Conflicts)
	sort.Strings(r.HomePaths)
	return r, nil
}

func isInstructions(rel string) bool {
	parts := layout(rel)
	return len(parts) == 1 && strings.HasPrefix(parts[0], "instructions") && strings.HasSuffix(parts[0], ".md")
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = fmt.Sprint(n)
	}
	return strings.Join(s, ", ")
}
