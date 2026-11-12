package secrets

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Scrubber replaces secret values with [NAME] in a stream. a value split across
// two writes is still caught: any tail that could start a value is held back
// until the next write or Flush. overlapping values are replaced as one span,
// named [A+B] in the order they start, so no part of either shows.
type Scrubber struct {
	mu    sync.Mutex
	w     io.Writer
	pats  []pattern
	buf   []byte
	maxLn int
}

type pattern struct {
	val  []byte
	name string
}

// past this much held output, an overlapping run is replaced as far as it got
const maxHold = 64 << 10

func NewScrubber(w io.Writer, values map[string]string) *Scrubber {
	s := &Scrubber{w: w}
	for name, v := range values {
		for _, f := range forms(v) {
			s.pats = append(s.pats, pattern{val: []byte(f), name: name})
			if len(f) > s.maxLn {
				s.maxLn = len(f)
			}
		}
	}
	// longest first so a value containing another wins at the same offset
	sort.Slice(s.pats, func(i, j int) bool { return len(s.pats[i].val) > len(s.pats[j].val) })
	return s
}

func (s *Scrubber) Write(p []byte) (int, error) {
	if len(s.pats) == 0 {
		return s.w.Write(p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	out, rest := s.scan(s.buf, false)
	s.buf = append(s.buf[:0], rest...)
	if len(out) > 0 {
		if _, err := s.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush writes whatever is held back. call it once the stream ends.
func (s *Scrubber) Flush() error {
	if len(s.pats) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out, _ := s.scan(s.buf, true)
	s.buf = s.buf[:0]
	if len(out) == 0 {
		return nil
	}
	_, err := s.w.Write(out)
	return err
}

// scan replaces full matches and returns (safe output, held-back tail).
func (s *Scrubber) scan(b []byte, final bool) (out, rest []byte) {
	for {
		pos, pi := -1, -1
		for i, p := range s.pats {
			if j := bytes.Index(b, p.val); j >= 0 && (pos < 0 || j < pos) {
				pos, pi = j, i
			}
		}
		end, names := 0, []string(nil)
		if pos >= 0 {
			end, names = s.span(b, pos, pos+len(s.pats[pi].val), s.pats[pi].name)
		}
		if !final {
			// a value may still be arriving at or before this match, or overlapping it
			if hold := s.heldTail(b); hold > 0 {
				q := len(b) - hold
				if pos < 0 || q <= pos {
					return append(out, b[:q]...), b[q:]
				}
				if q < end && len(b)-pos < maxHold {
					return append(out, b[:pos]...), b[pos:]
				}
			}
		}
		if pos < 0 {
			return append(out, b...), nil
		}
		out = append(out, b[:pos]...)
		out = append(out, "["+strings.Join(names, "+")+"]"...)
		b = b[end:]
	}
}

// span grows the match [pos, end) over every match that starts inside it and
// ends past it. returns the new end and the names, first one first.
func (s *Scrubber) span(b []byte, pos, end int, first string) (int, []string) {
	names := []string{first}
	for i := pos + 1; i < end; i++ {
		for _, p := range s.pats {
			if i+len(p.val) > end && bytes.HasPrefix(b[i:], p.val) {
				end = i + len(p.val)
				if !slices.Contains(names, p.name) {
					names = append(names, p.name)
				}
			}
		}
	}
	return end, names
}

// ScrubText scrubs a whole string the way the stream does.
func ScrubText(text string, values map[string]string) string {
	var b bytes.Buffer
	sc := NewScrubber(&b, values)
	_, _ = sc.Write([]byte(text))
	_ = sc.Flush()
	return b.String()
}

// heldTail is the length of the longest suffix of b that is a proper prefix of a value.
func (s *Scrubber) heldTail(b []byte) int {
	n := min(len(b), s.maxLn-1)
	for k := n; k > 0; k-- {
		tail := b[len(b)-k:]
		for _, p := range s.pats {
			if len(p.val) > k && bytes.HasPrefix(p.val, tail) {
				return k
			}
		}
	}
	return 0
}

// minLine is the shortest line of a multi-line value scrubbed on its own.
// shorter lines would match ordinary output.
const minLine = 8

// forms are the spellings of v worth catching: v, v trimmed, each long
// enough line of a multi-line v, and v json-escaped. a deliberate transform
// (rev, base64, a cut) still gets through; nothing here can stop that.
func forms(v string) []string {
	if v == "" {
		return nil
	}
	out := []string{v}
	add := func(f string) {
		if f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	add(strings.TrimSpace(v))
	if strings.Contains(v, "\n") {
		for _, l := range strings.Split(v, "\n") {
			if l = strings.TrimSpace(l); len(l) >= minLine {
				add(l)
			}
		}
	}
	for _, html := range []bool{true, false} {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(html)
		if enc.Encode(v) == nil {
			q := strings.TrimSuffix(b.String(), "\n")
			add(q[1 : len(q)-1])
		}
	}
	return out
}
