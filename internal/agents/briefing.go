package agents

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zacharytgray/yonder/internal/registry"
)

// the briefing: what every agent should know about the user's machines,
// rendered first in yonder's block. the machine list is generated from the
// records; agents/briefing.md holds the hand-written rest.
const (
	BriefingFile = "briefing.md"
	BriefingCap  = 4 << 10 // rendered, generated and hand-written together
	BriefingWarn = 3 << 10
	HandCap      = 2 << 10 // briefing.md as rendered
	ReviewMaxAge = 90 * 24 * time.Hour
)

const briefingTitle = "## Yonder briefing"

var reviewedRe = regexp.MustCompile(`^\s*reviewed:\s*(\d{4}-\d{2}-\d{2})\s*$`)

// BriefingSection is one heading's part of briefing.md.
type BriefingSection struct {
	Heading  string    `json:"heading"` // "" for text before the first heading
	Line     int       `json:"line"`    // where it starts, 1-based
	Reviewed time.Time `json:"reviewed"`
	Bytes    int       `json:"bytes"`
}

// parseBriefing splits briefing.md into sections and returns the text that
// renders: everything but the reviewed: lines. a reviewed: line before the
// first heading dates the whole file; one under a heading dates that section.
func parseBriefing(data []byte) ([]BriefingSection, string) {
	var (
		secs     []BriefingSection
		out      []string
		fileDate time.Time
		cur      *BriefingSection
	)
	for i, line := range strings.Split(string(data), "\n") {
		if m := reviewedRe.FindStringSubmatch(line); m != nil {
			d, err := time.Parse(time.DateOnly, m[1])
			if err == nil {
				if cur == nil || cur.Heading == "" {
					fileDate = d
				} else {
					cur.Reviewed = d
				}
			}
			continue
		}
		if isHeading(line) {
			secs = append(secs, BriefingSection{Heading: strings.TrimSpace(strings.TrimLeft(line, "#")), Line: i + 1})
			cur = &secs[len(secs)-1]
		} else if cur == nil && strings.TrimSpace(line) != "" {
			secs = append(secs, BriefingSection{Line: i + 1})
			cur = &secs[len(secs)-1]
		}
		if cur != nil {
			cur.Bytes += len(line) + 1
		}
		out = append(out, line)
	}
	for i := range secs {
		if secs[i].Reviewed.IsZero() {
			secs[i].Reviewed = fileDate
		}
	}
	return secs, strings.TrimSpace(strings.Join(out, "\n"))
}

func isHeading(line string) bool {
	t := strings.TrimLeft(line, "#")
	return len(t) < len(line) && len(line)-len(t) <= 6 && strings.HasPrefix(t, " ")
}

// how much of each record the machine list says. it steps down to fit its
// share of the budget.
const (
	detailFull     = iota // everything
	detailModels          // served models as a count
	detailHardware        // no OS, CPU, GPU or RAM either
	detailMinimal         // name, role and address
)

// GeneratedBriefing is the machine list, from the records only. nothing in it
// is hand-written, so it can't go stale while the records are fresh.
func GeneratedBriefing(ms []registry.Machine, self string) string {
	return generated(ms, self, detailFull)
}

// fitGenerated is the most detailed machine list that fits in budget bytes,
// or the shortest one when none does.
func fitGenerated(ms []registry.Machine, self string, budget int) (string, int) {
	var text string
	for d := detailFull; d <= detailMinimal; d++ {
		if text = generated(ms, self, d); len(text) <= budget {
			return text, d
		}
	}
	return text, detailMinimal
}

func generated(ms []registry.Machine, self string, detail int) string {
	var keep []registry.Machine
	for _, m := range ms {
		if registry.ValidName(m.Name) == nil {
			keep = append(keep, m)
		}
	}
	if len(keep) == 0 {
		return ""
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].Name < keep[j].Name })
	var b strings.Builder
	b.WriteString(briefingTitle + "\n\n")
	b.WriteString("Machines (generated from yonder's records; live state: `yonder ls`, details: `yonder show <machine>`):\n")
	users := map[string]bool{}
	for _, m := range keep {
		if u := cleanUser(m.SSHUser); u != "" {
			users[u] = true
		}
	}
	for _, m := range keep {
		b.WriteString(machineLine(m, self, len(users) > 1, detail) + "\n")
	}
	if detail > detailFull {
		b.WriteString("This list is shortened to fit the briefing's budget; `yonder ls` and `yonder show <machine>` have the rest.\n")
	}
	if len(users) == 1 {
		for u := range users {
			fmt.Fprintf(&b, "All reached as user `%s` over Tailscale. ", u)
		}
	}
	b.WriteString("Run with `yonder run <machine> -- <cmd>`, copy with `yonder cp`, long jobs with `--detach` and `yonder jobs`. Exit 75 means the machine is offline or asleep: say so and stop.\n")
	return b.String()
}

// the roles `yonder set` takes; anything else in a record is left out
var knownRoles = []string{"laptop", "desktop", "server", "gpu", "other"}

func machineLine(m registry.Machine, self string, user bool, detail int) string {
	head := "`" + m.Name + "`"
	var tags []string
	if contains(knownRoles, m.Role) {
		tags = append(tags, m.Role)
	}
	if m.Name == self {
		tags = append(tags, "this machine")
	}
	if m.Pending {
		tags = append(tags, "setup unfinished")
	}
	if len(tags) > 0 {
		head += " " + strings.Join(tags, ", ")
	}
	parts := []string{head}
	addr := ""
	if ip := net.ParseIP(m.TailscaleIP); ip != nil {
		addr = ip.String()
	}
	if u := cleanUser(m.SSHUser); user && u != "" && addr != "" {
		addr = u + "@" + addr
	}
	if addr != "" {
		parts = append(parts, addr)
	}
	if detail == detailMinimal {
		return "- " + strings.Join(parts, " · ")
	}
	f := m.Facts
	if detail < detailHardware {
		osText := strings.TrimSpace(clean(orStr(f.OSVersion, m.OS), 40) + " " + clean(orStr(f.Arch, m.Arch), 12))
		if osText != "" {
			parts = append(parts, osText)
		}
		if hw := hardwareText(f); hw != "" {
			parts = append(parts, hw)
		}
	}
	if ms := servedText(f.Models, detail > detailFull); ms != "" {
		parts = append(parts, ms)
	}
	if w := availability(m); w != "" {
		parts = append(parts, w)
	}
	return "- " + strings.Join(parts, " · ")
}

func orStr(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

// hardwareText: cpu, gpus with their memory, ram. an apple gpu that is the cpu
// is said once.
func hardwareText(f registry.Facts) string {
	var parts []string
	cpu := clean(registry.ShortCPU(clean(f.CPU, 120)), 40)
	if cpu != "" {
		parts = append(parts, cpu)
	}
	for _, g := range f.GPUs {
		n := clean(registry.ShortGPU(clean(g.Name, 120)), 40)
		switch {
		case n == "" || n == cpu:
		case g.Unified || g.MemoryGB < 2:
			if g.MemoryGB >= 2 || cpu == "" {
				parts = append(parts, n)
			}
		default:
			parts = append(parts, n+" "+registry.GBText(g.MemoryGB))
		}
	}
	if f.MemoryGB > 0 {
		parts = append(parts, registry.GBText(f.MemoryGB)+" RAM")
	}
	return strings.Join(parts, ", ")
}

const maxModels = 4

// servedText lists the models a server answered for at discovery time, or
// only how many when short.
func servedText(ms []registry.Model, short bool) string {
	var s []string
	for _, m := range ms {
		if !m.Serving {
			continue
		}
		t := clean(m.Name, 60)
		var tags []string
		if r := clean(m.Runtime, 16); r != "" {
			tags = append(tags, r)
		}
		if m.Port > 0 && m.Port < 65536 {
			tags = append(tags, fmt.Sprintf(":%d", m.Port))
		}
		if len(tags) > 0 {
			t += " (" + strings.Join(tags, " ") + ")"
		}
		s = append(s, t)
	}
	switch {
	case len(s) == 0:
		return ""
	case short && len(s) == 1:
		return "serves 1 model"
	case short:
		return fmt.Sprintf("serves %d models", len(s))
	}
	more := ""
	if len(s) > maxModels {
		more = fmt.Sprintf(" and %d more", len(s)-maxModels)
		s = s[:maxModels]
	}
	return "serves " + strings.Join(s, ", ") + more
}

// clean makes a record's text safe for the block: one line, no control,
// format (bidi, zero-width) or line-separator characters, no backticks,
// single spaces, at most max runes. records come from other machines.
func clean(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unsafeRune(r) || r == '`':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if rs := []rune(out); len(rs) > max {
		out = strings.TrimSpace(string(rs[:max-1])) + "…"
	}
	return out
}

// unsafeRune: what never goes into a rendered line or a model name: control
// characters (escape included), format characters (bidi, zero-width) and the
// unicode line and paragraph separators.
func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' || r == utf8.RuneError
}

var userRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// cleanUser is an ssh user name, or "" when the record's isn't one.
func cleanUser(u string) string {
	if userRe.MatchString(u) {
		return u
	}
	return ""
}

// checkGenerated refuses a machine list that holds a block marker or a
// placeholder: cleaned records shouldn't, so one that does is not rendered.
func checkGenerated(text string) error {
	switch {
	case strings.Contains(text, markerPrefix):
		return errors.New("the machine list would hold yonder's block marker (" + markerPrefix + "...), from a machine record; it is left out until the record is fixed (yonder refresh <machine>)")
	case strings.Contains(text, "{{model:"):
		return errors.New("the machine list would hold a {{model:...}} placeholder, from a machine record; it is left out until the record is fixed (yonder refresh <machine>)")
	}
	return nil
}

// availability is one word from the role and notes: always-on, sleeps,
// intermittent or travels. empty when they say nothing about it.
func availability(m registry.Machine) string {
	n := strings.ToLower(m.Notes)
	switch {
	case strings.Contains(n, "always on") || strings.Contains(n, "always-on") || strings.Contains(n, "24/7"):
		return "always-on"
	case strings.Contains(n, "sleep") || strings.Contains(n, "night"):
		return "sleeps"
	case strings.Contains(n, "offline") || strings.Contains(n, "sometimes on"):
		return "intermittent"
	case m.Role == "laptop" || m.Facts.Laptop:
		return "travels"
	case m.Role == "server":
		return "always-on"
	}
	return ""
}

// BriefingUsage is the rendered briefing against its budget.
type BriefingUsage struct {
	Bytes     int    `json:"bytes"`
	Warn      int    `json:"warn"`
	Cap       int    `json:"cap"`
	HandBytes int    `json:"hand_bytes"`
	HandCap   int    `json:"hand_cap"`
	Shortened bool   `json:"shortened"`       // the machine list was cut down to fit its share
	Error     string `json:"error,omitempty"` // why it doesn't render
}

func (u BriefingUsage) String() string {
	s := fmt.Sprintf("briefing %s / %s (warn at %s), hand-written %s / %s",
		kb(u.Bytes), kb(u.Cap), kb(u.Warn), kb(u.HandBytes), kb(u.HandCap))
	if u.Shortened {
		s += ", machine list shortened to fit"
	}
	return s
}

func kb(n int) string { return fmt.Sprintf("%.1f KB", float64(n)/1024) }

// machines is the registry's records, none when the linker has no registry.
func (l *Linker) machines() []registry.Machine {
	if l.Registry == nil {
		return nil
	}
	ms, _ := l.Registry.Machines()
	return ms
}

func (l *Linker) briefingSource() ([]byte, bool) {
	data, err := os.ReadFile(filepath.Join(l.StoreDir, BriefingFile))
	return data, err == nil
}

// briefing renders the briefing for one harness: the machine list, then
// briefing.md. over a cap it renders nothing and says why; it never cuts.
//
// the shares: the hand-written part has its own 2 KB cap, and the machine
// list gets the rest of the 4 KB, so at least 2 KB. the list steps down to
// fit its share (models to a count, then no hardware, then name, role and
// address), so a fleet that grows or starts serving models doesn't need an
// edit. only a list too long even at its shortest goes over.
func (l *Linker) briefing(key string) (string, BriefingUsage, error) {
	u := BriefingUsage{Warn: BriefingWarn, Cap: BriefingCap, HandCap: HandCap}
	var hand string
	var secs []BriefingSection
	data, ok := l.briefingSource()
	if ok {
		secs, hand = parseBriefing(data)
		var err error
		if err = l.tierError(key, textsBriefing); err == nil {
			hand, err = l.resolve(hand, key)
		}
		if err != nil {
			return "", u, err
		}
	}
	u.HandBytes = len(hand)
	if u.HandBytes > HandCap {
		return "", u, fmt.Errorf("%s; %s", handCapReason(u.HandBytes, secs), trimHint)
	}
	share := BriefingCap - len(hand) - 2 // the blank line between, and the last newline
	gen, detail := fitGenerated(l.machines(), l.Self, share)
	u.Shortened = detail > detailFull
	if err := checkGenerated(gen); err != nil {
		return "", u, err
	}
	if gen == "" && hand == "" {
		return "", u, nil
	}
	if gen == "" {
		hand = briefingTitle + "\n\n" + hand
	}
	text := strings.TrimRight(gen+"\n"+hand, "\n") + "\n"
	text = strings.TrimLeft(text, "\n")
	u.Bytes = len(text)
	if u.Bytes > BriefingCap {
		return "", u, fmt.Errorf("the briefing is %s, over its %s cap even with the machine list at its shortest (machine list %s, hand-written %s%s); the instructions render without it until it fits; trim agents/%s, or remove machines that are gone", kb(u.Bytes), kb(BriefingCap), kb(len(gen)), kb(u.HandBytes), largest(secs), BriefingFile)
	}
	if ok {
		// a block marker or a key in it: it can't sync, so it isn't rendered either
		if r := Check(BriefingFile, data); r != nil && r.Final {
			return "", u, fmt.Errorf("agents/%s can't render: %s", BriefingFile, r.Reason)
		}
	}
	return text, u, nil
}

// handCapReason: briefing.md over its cap, with the biggest sections
func handCapReason(n int, secs []BriefingSection) string {
	return fmt.Sprintf("the hand-written briefing (agents/%s) is %s, over its %s cap%s", BriefingFile, kb(n), kb(HandCap), largest(secs))
}

const trimHint = "the instructions render without the briefing, and nothing was cut; move detail into a doc or a skill and leave a one-line pointer"

// largest names the three biggest sections, for the over-cap error.
func largest(secs []BriefingSection) string {
	s := append([]BriefingSection(nil), secs...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].Bytes > s[j].Bytes })
	if len(s) > 3 {
		s = s[:3]
	}
	var parts []string
	for _, sec := range s {
		parts = append(parts, fmt.Sprintf("%q %d B", orStr(sec.Heading, "(top)"), sec.Bytes))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; largest sections: " + strings.Join(parts, ", ")
}

// Briefing renders the briefing as harness h gets it.
func (l *Linker) Briefing(h string) (string, BriefingUsage, error) {
	key := "claude"
	if w, ok := wirings[h]; ok {
		key = w.key
	}
	return l.briefing(key)
}

// BriefingUsage is the largest rendered briefing across the harnesses linked
// here (claude's when none is), against the budget.
func (l *Linker) BriefingUsage() BriefingUsage {
	keys := []string{"claude"}
	if st, err := l.State(); err == nil && len(st.Harnesses) > 0 {
		keys = nil
		for _, h := range st.Harnesses {
			if w, ok := wirings[h]; ok && w.instructions != "" {
				keys = append(keys, w.key)
			}
		}
	}
	var best BriefingUsage
	for i, k := range keys {
		_, u, err := l.briefing(k)
		if err != nil {
			u.Error = err.Error()
		}
		// a failure, once seen, outranks any briefing that rendered
		switch {
		case i == 0:
			best = u
		case (u.Error != "") != (best.Error != ""):
			if u.Error != "" {
				best = u
			}
		case u.Bytes > best.Bytes:
			best = u
		}
	}
	if len(keys) == 0 {
		best = BriefingUsage{Warn: BriefingWarn, Cap: BriefingCap, HandCap: HandCap}
	}
	return best
}
