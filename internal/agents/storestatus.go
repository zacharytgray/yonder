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
	Stale     []string             `json:"stale_peers"`        // machines last synced longer ago than deletes are kept
	Held      string               `json:"held"`               // why the next sync would stop here: a missing folder or a mass delete
	Briefing  BriefingUsage        `json:"briefing"`           // the rendered briefing against its budget
}

// StoreReport reads the store's state without changing anything.
func StoreReport(self string) (StoreStatus, error) {
	r := StoreStatus{
		LayerOn: registry.Open().LayerOn(registry.LayerAgents), Review: ReviewMode(),
		Linked: []string{}, Pending: []string{}, Conflicts: []string{}, Refused: []store.Refusal{},
		LastSync: map[string]time.Time{}, Notices: []store.Notice{}, HomePaths: []string{}, Problems: []string{}, Large: []string{}, Stale: []string{},
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
		if isInstructions(rel) || isBriefing(rel) {
			if data, ok := s.Live(rel); ok {
				if lines := HomePaths(data, OwnHome()); len(lines) > 0 {
					r.HomePaths = append(r.HomePaths, fmt.Sprintf("%s (line %s)", rel, joinInts(lines)))
				}
			}
		}
	}
	for p := range st.Pending {
		r.Pending = append(r.Pending, p)
	}
	for m, t := range st.Peers {
		if time.Since(t) > store.DeleteTTL {
			r.Stale = append(r.Stale, fmt.Sprintf("%s (%d days)", m, int(time.Since(t).Hours()/24)))
		}
	}
	sort.Strings(r.Stale)
	r.Held, _ = s.Held()
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
	r.Briefing = l.BriefingUsage()
	sort.Strings(r.Pending)
	sort.Strings(r.Conflicts)
	sort.Strings(r.HomePaths)
	return r, nil
}

// isBriefing: the hand-written briefing, at the top of the store only
func isBriefing(rel string) bool { return strings.EqualFold(rel, BriefingFile) }

// OwnHome is this machine's real home, for the home-path check. it reads no
// files, so tests under YONDER_HOME may call it.
func OwnHome() string {
	h, _ := os.UserHomeDir()
	return h
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
