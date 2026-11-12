// Package mesh lets every fleet machine ssh into every other one: it adds
// the client keys on record to each machine's authorized_keys, tagged so
// remove can find them again.
package mesh

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

// TagPrefix starts the trailing comment on every line mesh adds: "yonder:<machine>".
const TagPrefix = "yonder:"

var (
	typeRe = regexp.MustCompile(`^[a-z0-9@.-]+$`)
	b64Re  = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,3}$`)
)

// Grant is one key to authorize, and the machine it belongs to.
type Grant struct {
	Machine string
	Key     string // "type base64", no options or comment
}

// Line is the authorized_keys line for g: key, then the tag. never options.
func (g Grant) Line() string { return g.Key + " " + TagPrefix + g.Machine }

// cleanKey is key's "type base64" when it is safe to write as a line.
func cleanKey(key string) (string, bool) {
	id, ok := transport.KeyID(key)
	if !ok {
		return "", false
	}
	typ, b64, _ := strings.Cut(id, " ")
	if !typeRe.MatchString(typ) || !b64Re.MatchString(b64) {
		return "", false
	}
	return id, true
}

// TagOf is the machine named by a yonder tag at the end of line, or "".
func TagOf(line string) string {
	if _, ok := transport.KeyID(line); !ok {
		return ""
	}
	f := strings.Fields(line)
	last := f[len(f)-1]
	if !strings.HasPrefix(last, TagPrefix) {
		return ""
	}
	return strings.TrimPrefix(last, TagPrefix)
}

// present is every key in content, commented-out lines included: a key the
// user disabled by hand stays disabled.
func present(content string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "# \t")
		if id, ok := transport.KeyID(line); ok {
			out[id] = true
		}
	}
	return out
}

// Missing is the grants whose key isn't in content yet.
func Missing(content string, gs []Grant) []Grant {
	have := present(content)
	var out []Grant
	for _, g := range gs {
		if !have[g.Key] {
			have[g.Key] = true
			out = append(out, g)
		}
	}
	return out
}

// Append adds the missing grants to content, and never changes a line already there.
func Append(content string, gs []Grant) (string, []string) {
	var lines []string
	for _, g := range Missing(content, gs) {
		lines = append(lines, g.Line())
	}
	if len(lines) == 0 {
		return content, nil
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + strings.Join(lines, "\n") + "\n", lines
}

// StripTag drops the lines tagged for name whose key is not in keep.
// every other line stays byte for byte.
func StripTag(content, name string, keep map[string]bool) (string, []string) {
	var b strings.Builder
	var gone []string
	for _, line := range strings.SplitAfter(content, "\n") {
		if TagOf(line) == name {
			if id, _ := transport.KeyID(line); !keep[id] {
				gone = append(gone, strings.TrimRight(line, "\r\n"))
				continue
			}
		}
		b.WriteString(line)
	}
	return b.String(), gone
}

// Keep is every key a live record lists, except the named machine's own.
func Keep(live []registry.Machine, except string) map[string]bool {
	out := map[string]bool{}
	for _, m := range live {
		if m.Name == except {
			continue
		}
		for _, k := range m.SSHClientKeys {
			if id, ok := transport.KeyID(k); ok {
				out[id] = true
			}
		}
	}
	return out
}

// Source is a machine whose keys mesh may hand out.
type Source struct {
	Name string
	Keys []string // "type base64"
}

// Skip is a machine left out as a source, and why.
type Skip struct{ Name, Why string }

// Sources picks the records whose keys mesh may authorize elsewhere: set up
// (so sync can no longer change their keys), not removed, still the tailnet
// node they were added as, and owned by this tailscale account unless
// allowShared. a key any lost tombstone lists is never handed out.
func Sources(self string, ms []registry.Machine, ts []registry.Tombstone, st *tailnet.Status, allowShared bool) ([]Source, []Skip) {
	gone := map[string]bool{}
	lost := map[string]bool{}
	for _, t := range ts {
		gone[t.Name] = true
		if t.Lost {
			for _, k := range append(append([]string{}, t.SSHClientKeys...), t.HeldKeys...) {
				if id, ok := transport.KeyID(k); ok {
					lost[id] = true
				}
			}
		}
	}
	var src []Source
	var skip []Skip
	for _, m := range ms {
		why := ""
		switch {
		case gone[m.Name]:
			why = "it was removed"
		case m.Pending || m.SSHHostKey == "":
			why = "its setup is unfinished; run `yonder add " + m.Name + "`"
		case m.Name != self && st == nil:
			why = "tailscale status is unavailable"
		case m.Name != self:
			p, ok := st.ForRecord(m.Name, m.DNSName, m.TailscaleIP)
			switch {
			case !ok:
				why = "it is not in tailscale status"
			case p.IP() != m.TailscaleIP:
				why = fmt.Sprintf("its tailnet address changed (record %s, tailnet %s); run `yonder doctor`", m.TailscaleIP, p.IP())
			case !st.Owned(p) && !allowShared:
				why = "your tailscale account doesn't own it (" + st.NotOwnedWhy(p) + "); --allow-shared includes it"
			}
		}
		if why != "" {
			skip = append(skip, Skip{m.Name, why})
			continue
		}
		s := Source{Name: m.Name}
		seen := map[string]bool{}
		for _, k := range m.SSHClientKeys {
			if id, ok := cleanKey(k); ok && !lost[id] && !seen[id] {
				seen[id] = true
				s.Keys = append(s.Keys, id)
			}
		}
		if len(s.Keys) == 0 {
			skip = append(skip, Skip{m.Name, "no ssh client keys on record"})
			continue
		}
		src = append(src, s)
	}
	return src, skip
}

// Grants is what target should trust: every other source's keys. focus, when
// set, keeps only the pairs that involve that machine.
func Grants(target, focus string, src []Source) []Grant {
	var out []Grant
	for _, s := range src {
		if s.Name == target || (focus != "" && target != focus && s.Name != focus) {
			continue
		}
		for _, k := range s.Keys {
			out = append(out, Grant{Machine: s.Name, Key: k})
		}
	}
	return out
}

// Unreached names the sources that can't ssh into target: none of their keys
// is in its authorized_keys.
func Unreached(content, target string, src []Source) []string {
	have := present(content)
	var out []string
	for _, s := range src {
		if s.Name == target {
			continue
		}
		found := false
		for _, k := range s.Keys {
			found = found || have[k]
		}
		if !found {
			out = append(out, s.Name)
		}
	}
	return out
}

// Leftover counts, per removed machine, the lines mesh added for it that are still in content.
func Leftover(content string, ts []registry.Tombstone, live []registry.Machine) map[string]int {
	out := map[string]int{}
	for _, t := range ts {
		keep := Keep(live, t.Name)
		if t.IncludeShared {
			keep = nil
		}
		if _, gone := StripTag(content, t.Name, keep); len(gone) > 0 {
			out[t.Name] = len(gone)
		}
	}
	return out
}

// StripRemoved drops the lines mesh added for every removed machine from this
// machine's authorized_keys, keeping a key a live record also lists unless the
// tombstone has include_shared. one backup for all of them. sync runs it.
func StripRemoved(ts []registry.Tombstone, live []registry.Machine) ([]string, error) {
	if len(ts) == 0 {
		return nil, nil
	}
	counts := map[string]int{}
	_, _, err := transport.EditAuthorizedKeys(transport.AuthorizedKeysPath(registry.UserHome()), false, func(content string) (string, []string) {
		var all []string
		for _, t := range ts {
			keep := Keep(live, t.Name)
			if t.IncludeShared {
				keep = nil
			}
			var gone []string
			content, gone = StripTag(content, t.Name, keep)
			counts[t.Name] = len(gone)
			all = append(all, gone...)
		}
		return content, all
	})
	if err != nil {
		return nil, fmt.Errorf("removing mesh lines of removed machines: %w", err)
	}
	var out []string
	names := make([]string, 0, len(counts))
	for n, c := range counts {
		if c > 0 {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, fmt.Sprintf("removed %d line(s) yonder added for %s from ~/.ssh/authorized_keys", counts[n], n))
	}
	return out, nil
}
