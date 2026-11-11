package registry

import (
	"sort"
	"time"
)

// Bundle is the full registry: what _sync sends and receives.
type Bundle struct {
	Machines   []Machine   `json:"machines"`
	Tombstones []Tombstone `json:"tombstones"`
	// names whose lost tombstone the receiver should drop: a local `yonder add` re-added them
	ClearLost []string `json:"clear_lost,omitempty"`
}

// MaxSkew is how far ahead of this clock an incoming timestamp may be.
const MaxSkew = 5 * time.Minute

// Merge combines bundles per name. newest record wins and newest tombstone wins.
// a tombstone drops the record unless the machine was re-added (added_at) after
// the removal, so a removed machine refreshing its own record can't bring it back.
// a lost tombstone always drops the record; only a local `yonder add` clears it.
func Merge(bundles ...Bundle) Bundle {
	recs := map[string]Machine{}
	tombs := map[string]Tombstone{}
	for _, b := range bundles {
		for _, m := range b.Machines {
			cur, ok := recs[m.Name]
			if !ok {
				recs[m.Name] = m
				continue
			}
			added := laterOf(cur.AddedAt, m.AddedAt)
			if m.UpdatedAt.After(cur.UpdatedAt) {
				cur = m
			}
			cur.AddedAt = added
			recs[m.Name] = cur
		}
		for _, t := range b.Tombstones {
			cur, ok := tombs[t.Name]
			if !ok || t.RemovedAt.After(cur.RemovedAt) || (t.RemovedAt.Equal(cur.RemovedAt) && t.Lost && !cur.Lost) {
				tombs[t.Name] = t
			}
		}
	}
	for name, t := range tombs {
		m, ok := recs[name]
		if !ok {
			continue
		}
		if t.Lost || m.AddedAt.Before(t.RemovedAt) {
			delete(recs, name)
		} else {
			delete(tombs, name) // re-added after removal
		}
	}
	out := Bundle{Machines: []Machine{}, Tombstones: []Tombstone{}}
	for _, m := range recs {
		out.Machines = append(out.Machines, m)
	}
	for _, t := range tombs {
		out.Tombstones = append(out.Tombstones, t)
	}
	sort.Slice(out.Machines, func(i, j int) bool { return out.Machines[i].Name < out.Machines[j].Name })
	sort.Slice(out.Tombstones, func(i, j int) bool { return out.Tombstones[i].Name < out.Tombstones[j].Name })
	return out
}

// FromPeer filters a bundle a peer sent before it is merged into local.
// it drops anything about self (unless local has no self record yet) and
// anything dated more than MaxSkew ahead of now. for a record held here (and
// set up), identity fields keep their local values: changing them takes
// `yonder remove` then `yonder add`.
func FromPeer(local, in Bundle, self string, now time.Time) Bundle {
	held := map[string]Machine{}
	for _, m := range local.Machines {
		held[m.Name] = m
	}
	_, haveSelf := held[self]
	limit := now.Add(MaxSkew)
	out := Bundle{Machines: []Machine{}, Tombstones: []Tombstone{}}
	for _, m := range in.Machines {
		if m.UpdatedAt.After(limit) || m.AddedAt.After(limit) || (m.Name == self && haveSelf) {
			continue
		}
		if l, ok := held[m.Name]; ok && !l.Pending {
			m.TailscaleIP, m.SSHHostKey, m.SSHClientKeys, m.AddedAt = l.TailscaleIP, l.SSHHostKey, l.SSHClientKeys, l.AddedAt
		}
		out.Machines = append(out.Machines, m)
	}
	for _, t := range in.Tombstones {
		if t.Name == self || t.RemovedAt.After(limit) {
			continue
		}
		out.Tombstones = append(out.Tombstones, t)
	}
	return out
}

// Digest is a cheap fingerprint of a bundle: names and timestamps.
func (b Bundle) Digest() string {
	m := Merge(b)
	s := ""
	for _, r := range m.Machines {
		s += "m:" + r.Name + "@" + r.UpdatedAt.UTC().String() + ";"
	}
	for _, t := range m.Tombstones {
		s += "t:" + t.Name + "@" + t.RemovedAt.UTC().String() + ";"
	}
	return s
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
