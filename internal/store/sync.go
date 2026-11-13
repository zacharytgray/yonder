package store

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

// Peer is the other side of a sync: the hidden commands on another machine.
type Peer interface {
	Name() string
	Offer() (Manifest, error)             // _store-offer: it scans and says what it holds
	Fetch(paths []string) (Bundle, error) // _store-send: those paths as they are now
	Push(b Bundle) (PushResult, error)    // _store-recv: apply these
}

// Change is one path that changed here in a sync.
type Change struct {
	Path   string    `json:"path"`
	State  FileState `json:"state"`
	Action string    `json:"action"` // updated | deleted | pending
}

// Conflict is a concurrent edit: the winner stays at Path, the other is at Copy.
type Conflict struct {
	Path   string `json:"path"`
	Copy   string `json:"copy"`
	Winner string `json:"winner"` // machine
	Loser  string `json:"loser"`
}

// Report is one sync with one peer.
type Report struct {
	Peer        string     `json:"peer"`
	Got         []Change   `json:"got"`
	Sent        []string   `json:"sent"`
	PeerPending []string   `json:"peer_pending"` // sent, and waiting for review there
	Conflicts   []Conflict `json:"conflicts"`
	DeleteLost  []string   `json:"delete_lost"` // a delete that lost to an edit
	Skipped     []Skip     `json:"skipped"`
	PeerRefused []Refusal  `json:"peer_refused"` // files the peer won't sync, or wouldn't take
	DryRun      bool       `json:"dry_run"`

	holdThere []string // paths the peer has that it should hold for this machine
}

// Changed reports whether anything moved either way.
func (r Report) Changed() bool {
	return len(r.Got) > 0 || len(r.Sent) > 0 || len(r.Conflicts) > 0
}

type step struct {
	path      string
	here      *FileState // new state for this machine, nil for none
	there     *FileState // new state for the peer, nil for none
	needThere bool       // the content for here comes from the peer
	copyOf    string     // for a conflict copy: the path it came from
	fromPeer  bool       // the content for a conflict copy comes from the peer
}

// SyncWith syncs this store with one peer. callers hold the lock and have
// scanned. dry plans and reports without changing anything.
func (s *Store) SyncWith(p Peer, dry bool) (Report, error) {
	rep := Report{Peer: p.Name(), DryRun: dry}
	st, err := s.State()
	if err != nil {
		return rep, err
	}
	rm, err := p.Offer()
	if err != nil {
		return rep, err
	}
	if rm.V > ProtocolVersion {
		return rep, fmt.Errorf("%s speaks store protocol %d; upgrade yonder here", p.Name(), rm.V)
	}
	if rm.Store != s.Kind.Name {
		return rep, fmt.Errorf("%s answered for store %q", p.Name(), rm.Store)
	}
	rep.PeerRefused = rm.Refused
	// a pair that hasn't synced within DeleteTTL may each hold files the
	// other deleted and has forgotten. a file only one side has, unchanged
	// since the pair last met, waits; anything newer can't be one of those
	var heldUntil time.Time
	if last := laterOf(st.Peers[p.Name()], rm.LastWith); !s.StaleOK && stale(last, s.now()) {
		heldUntil = last
	}
	steps := s.plan(rm, &rep, heldUntil)

	if dry {
		for _, x := range steps {
			if x.here != nil {
				rep.Got = append(rep.Got, Change{Path: x.path, State: *x.here, Action: action(*x.here)})
			}
			if x.there != nil {
				rep.Sent = append(rep.Sent, x.path)
			}
		}
		return rep, nil
	}

	// fetch what the peer holds that this machine needs
	var want []string
	for _, x := range steps {
		if x.needThere {
			want = append(want, x.path)
		} else if x.fromPeer {
			want = append(want, x.copyOf)
		}
	}
	got := Bundle{Files: map[string]FileState{}, Data: map[string][]byte{}}
	if len(want) > 0 {
		if got, err = p.Fetch(dedupe(want)); err != nil {
			return rep, fmt.Errorf("fetching from %s: %w", p.Name(), err)
		}
	}
	unchanged := func(path string) bool {
		f, ok := got.Files[path]
		return ok && f.Version.Compare(rm.Files[path].Version) == Equal
	}

	var changed []string
	push := Bundle{Store: s.Kind.Name, From: s.Self, Files: map[string]FileState{}, Data: map[string][]byte{}}
	failedCopy := map[string]string{} // path -> why its conflict copy couldn't be written
	for _, x := range steps {
		if why, ok := failedCopy[x.path]; ok && x.copyOf == "" {
			// without the copy the losing edit would be lost, so neither side changes
			rep.Skipped = append(rep.Skipped, Skip{Path: x.path, Why: "two edits conflict, and the copy of one could not be written (" + why + "); both are left as they are"})
			rep.Conflicts = slices.DeleteFunc(rep.Conflicts, func(c Conflict) bool { return c.Path == x.path })
			continue
		}
		var data []byte
		switch {
		case x.needThere:
			if !unchanged(x.path) {
				rep.Skipped = append(rep.Skipped, Skip{Path: x.path, Why: "changed on " + p.Name() + " during the sync"})
				continue
			}
			data = got.Data[x.path]
		case x.fromPeer:
			if !unchanged(x.copyOf) {
				failedCopy[x.copyOf] = "it changed on " + p.Name() + " during the sync"
				continue
			}
			data = got.Data[x.copyOf]
		case x.copyOf != "":
			data = s.content(x.copyOf)
		}
		if x.here != nil {
			if x.copyOf != "" && data == nil {
				failedCopy[x.copyOf] = "it changed here during the sync"
				continue
			}
			// the content is this machine's own, unless it is a change that
			// still waits for review here: that came from another machine
			source := x.path
			if x.copyOf != "" {
				source = x.copyOf
			}
			_, waiting := s.st.Pending[source]
			local := !x.needThere && !x.fromPeer && !waiting
			what, err := s.apply(x.path, *x.here, data, p.Name(), local)
			var re refusedError
			switch {
			case errors.As(err, &re):
				re.r.Path = x.path
				rep.PeerRefused = append(rep.PeerRefused, Refusal{Path: x.path, Reason: "refused here: " + re.r.Reason, Fix: re.r.Fix})
				if x.copyOf != "" {
					failedCopy[x.copyOf] = x.path + " was refused: " + re.r.Reason
				}
				continue
			case err != nil:
				if x.copyOf != "" {
					failedCopy[x.copyOf] = x.path + ": " + err.Error()
					continue
				}
				rep.Skipped = append(rep.Skipped, Skip{Path: x.path, Why: err.Error()})
				continue
			case what != "":
				rep.Got = append(rep.Got, Change{Path: x.path, State: *x.here, Action: what})
				changed = append(changed, x.path)
			}
		}
		if x.there != nil {
			push.Files[x.path] = *x.there
			if theirs := rm.Files[x.path]; !x.there.Deleted && (theirs.Deleted || theirs.Hash != x.there.Hash) {
				d := s.content(x.path)
				if d == nil {
					delete(push.Files, x.path)
					rep.Skipped = append(rep.Skipped, Skip{Path: x.path, Why: "changed here during the sync"})
					continue
				}
				push.Data[x.path] = d
			}
		}
	}
	push.Hold = rep.holdThere
	if len(push.Files) > 0 || len(push.Hold) > 0 {
		res, err := p.Push(push)
		if err != nil {
			_ = s.Save() // keep what was applied here
			return rep, fmt.Errorf("sending to %s: %w", p.Name(), err)
		}
		rep.Sent = append(rep.Sent, res.Applied...)
		rep.Sent = append(rep.Sent, res.Pending...)
		rep.PeerPending = res.Pending
		rep.Skipped = append(rep.Skipped, res.Skipped...)
		for _, r := range res.Refused {
			r.Reason = "refused by " + p.Name() + ": " + r.Reason
			rep.PeerRefused = append(rep.PeerRefused, r)
		}
	}
	sort.Strings(rep.Sent)
	st.Peers[p.Name()] = s.now()
	if err := s.Save(); err != nil {
		return rep, err
	}
	if len(changed) > 0 && s.Kind.AfterChange != nil {
		s.Kind.AfterChange(s, changed)
	}
	return rep, nil
}

func action(f FileState) string {
	if f.Deleted {
		return "deleted"
	}
	return "updated"
}

func dedupe(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// plan compares the two manifests path by path.
func (s *Store) plan(rm Manifest, rep *Report, heldUntil time.Time) []step {
	// a file only one side has is held when the pair is stale and the file
	// is unchanged since it last met, or when it was held at an earlier
	// meeting and hasn't changed since: this side's record for a file it
	// has, the peer's flag for one the peer has
	mine := s.st.Held[rm.Machine]
	stale := func(f FileState) bool { return !heldUntil.IsZero() && !f.Origin.Time.After(heldUntil) }
	holdMine := func(path string, f FileState) bool {
		if s.StaleOK {
			return false
		}
		if t, ok := mine[path]; ok && !f.Origin.Time.After(t) {
			return true
		}
		if stale(f) {
			s.hold(rm.Machine, path)
			return true
		}
		return false
	}
	local := s.st.Files
	paths := map[string]bool{}
	for p := range local {
		paths[p] = true
	}
	for p := range rm.Files {
		if ValidRel(p) == nil {
			paths[p] = true
		}
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	taken := func(p string) bool {
		l, lok := local[p]
		r, rok := rm.Files[p]
		return (lok && !l.Deleted) || (rok && !r.Deleted)
	}

	var steps []step
	for _, path := range sorted {
		l, lok := local[path]
		r, rok := rm.Files[path]
		if lok && rok {
			delete(mine, path) // both know it now
		}
		switch {
		case !rok:
			if l.Deleted {
				continue
			}
			if holdMine(path, l) {
				rep.Skipped = append(rep.Skipped, Skip{Path: path, Why: staleWhy(rm.Machine)})
				continue
			}
			steps = append(steps, step{path: path, there: ptr(l)})
			continue
		case !lok:
			if r.Deleted {
				continue
			}
			if !s.StaleOK && (r.Held || stale(r)) {
				// the peer holds it for this machine from now on
				rep.holdThere = append(rep.holdThere, path)
				rep.Skipped = append(rep.Skipped, Skip{Path: path, Why: staleWhy(rm.Machine)})
				continue
			}
			r.Held = false
			steps = append(steps, step{path: path, here: ptr(r), needThere: true})
			continue
		}
		switch l.Version.Compare(r.Version) {
		case After:
			steps = append(steps, step{path: path, there: ptr(l)})
		case Before:
			steps = append(steps, step{path: path, here: ptr(r), needThere: !r.Deleted})
		case Equal:
			if l.sameContent(r) {
				continue
			}
			fallthrough // same counters, different content: treat as concurrent
		case Concurrent:
			merged := Max(l.Version, r.Version).Bump(s.Self)
			switch {
			case l.sameContent(r):
				f := l
				if r.Origin.Time.After(l.Origin.Time) {
					f = r
				}
				f.Version = Max(l.Version, r.Version)
				steps = append(steps, step{path: path, here: ptr(f), there: ptr(f)})
			case l.Deleted:
				f := r
				f.Version = merged
				steps = append(steps, step{path: path, here: ptr(f), there: ptr(f), needThere: true})
				rep.DeleteLost = append(rep.DeleteLost, path)
				s.notice("delete-lost", path, "deleted here, edited on "+rm.Machine+"; the edit was kept")
			case r.Deleted:
				f := l
				f.Version = merged
				steps = append(steps, step{path: path, here: ptr(f), there: ptr(f)})
				rep.DeleteLost = append(rep.DeleteLost, path)
				s.notice("delete-lost", path, "deleted on "+rm.Machine+", edited here; the edit was kept")
			default:
				win, lose, remoteWins := l, r, false
				if r.Origin.Time.After(l.Origin.Time) || (r.Origin.Time.Equal(l.Origin.Time) && r.Origin.Machine < l.Origin.Machine) {
					win, lose, remoteWins = r, l, true
				}
				main := win
				main.Version = merged
				// the copy is a new path; its counters beat any earlier delete there
				var cp string
				for n := 1; ; n++ {
					cp = ConflictName(path, orSelf(lose.Origin.Machine), lose.Origin.Time, n)
					if !taken(cp) {
						break
					}
				}
				cs := lose
				cs.Version = Max(local[cp].Version, rm.Files[cp].Version).Bump(s.Self)
				// the copy goes first, while the loser's content is still at path here
				steps = append(steps, step{path: cp, here: ptr(cs), there: ptr(cs), copyOf: path, fromPeer: !remoteWins})
				steps = append(steps, step{path: path, here: ptr(main), there: ptr(main), needThere: remoteWins})
				rep.Conflicts = append(rep.Conflicts, Conflict{Path: path, Copy: cp, Winner: win.Origin.Machine, Loser: lose.Origin.Machine})
				s.notice("conflict", path, "edited on "+l.Origin.Machine+" and "+r.Origin.Machine+"; the other version is at "+cp)
			}
		}
	}
	return steps
}

func ptr(f FileState) *FileState { return &f }

// stale: a pair last synced longer ago than delete markers are kept. a pair
// that never synced is new, not stale.
func stale(last, now time.Time) bool {
	return !last.IsZero() && now.Sub(last) > DeleteTTL
}

// hold records that path is held back from peer, as of now.
func (s *Store) hold(peer, path string) {
	if s.st.Held[peer] == nil {
		s.st.Held[peer] = map[string]time.Time{}
	}
	if _, ok := s.st.Held[peer][path]; !ok {
		s.st.Held[peer][path] = s.now()
	}
}

func staleWhy(peer string) string {
	return "held back: this machine and " + peer + " last synced longer ago than deletes are remembered, and only one of them has this file, so the other may have deleted it; `yonder sync " + peer + " --stale-ok` syncs it anyway"
}
