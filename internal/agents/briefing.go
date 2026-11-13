package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

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

// GeneratedBriefing is the machine list, from the records only. nothing in it
// is hand-written, so it can't go stale while the records are fresh.
func GeneratedBriefing(ms []registry.Machine, self string) string {
	if len(ms) == 0 {
		return ""
	}
	ms = append([]registry.Machine(nil), ms...)
	sort.Slice(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name })
	var b strings.Builder
	b.WriteString(briefingTitle + "\n\n")
	b.WriteString("Machines (generated from yonder's records; live state: `yonder ls`, details: `yonder show <machine>`):\n")
	users := map[string]bool{}
	for _, m := range ms {
		users[m.SSHUser] = true
	}
	for _, m := range ms {
		b.WriteString(machineLine(m, self, len(users) > 1) + "\n")
	}
	if len(users) == 1 && ms[0].SSHUser != "" {
		fmt.Fprintf(&b, "All reached as user `%s` over Tailscale. ", ms[0].SSHUser)
	}
	b.WriteString("Run with `yonder run <machine> -- <cmd>`, copy with `yonder cp`, long jobs with `--detach` and `yonder jobs`. Exit 75 means the machine is offline or asleep: say so and stop.\n")
	return b.String()
}

func machineLine(m registry.Machine, self string, user bool) string {
	head := "`" + m.Name + "`"
	var tags []string
	if m.Role != "" {
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
	addr := m.TailscaleIP
	if user && m.SSHUser != "" {
		addr = m.SSHUser + "@" + addr
	}
	if addr != "" {
		parts = append(parts, addr)
	}
	f := m.Facts
	osText := strings.TrimSpace(orStr(f.OSVersion, m.OS) + " " + orStr(f.Arch, m.Arch))
	if osText != "" {
		parts = append(parts, osText)
	}
	if hw := hardwareText(f); hw != "" {
		parts = append(parts, hw)
	}
	if ms := servedText(f.Models); ms != "" {
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
	cpu := registry.ShortCPU(f.CPU)
	if cpu != "" {
		parts = append(parts, cpu)
	}
	for _, g := range f.GPUs {
		n := registry.ShortGPU(g.Name)
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

// servedText lists the models a server answered for at discovery time.
func servedText(ms []registry.Model) string {
	var s []string
	for _, m := range ms {
		if !m.Serving {
			continue
		}
		t := m.Name
		var tags []string
		if m.Runtime != "" {
			tags = append(tags, m.Runtime)
		}
		if m.Port > 0 {
			tags = append(tags, fmt.Sprintf(":%d", m.Port))
		}
		if len(tags) > 0 {
			t += " (" + strings.Join(tags, " ") + ")"
		}
		s = append(s, t)
	}
	if len(s) == 0 {
		return ""
	}
	more := ""
	if len(s) > maxModels {
		more = fmt.Sprintf(" and %d more", len(s)-maxModels)
		s = s[:maxModels]
	}
	return "serves " + strings.Join(s, ", ") + more
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
	Error     string `json:"error,omitempty"` // why it doesn't render
}

func (u BriefingUsage) String() string {
	return fmt.Sprintf("briefing %s / %s (warn at %s), hand-written %s / %s",
		kb(u.Bytes), kb(u.Cap), kb(u.Warn), kb(u.HandBytes), kb(u.HandCap))
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
func (l *Linker) briefing(key string) (string, BriefingUsage, error) {
	u := BriefingUsage{Warn: BriefingWarn, Cap: BriefingCap, HandCap: HandCap}
	gen := GeneratedBriefing(l.machines(), l.Self)
	var hand string
	var secs []BriefingSection
	data, ok := l.briefingSource()
	if ok {
		secs, hand = parseBriefing(data)
		var err error
		if err = l.tierError(key); err == nil {
			hand, err = l.resolve(hand, key)
		}
		if err != nil {
			return "", u, err
		}
	}
	if gen == "" && hand == "" {
		return "", u, nil
	}
	if gen == "" {
		hand = briefingTitle + "\n\n" + hand
	}
	text := strings.TrimRight(gen+"\n"+hand, "\n") + "\n"
	text = strings.TrimLeft(text, "\n")
	u.Bytes, u.HandBytes = len(text), len(hand)
	switch {
	case u.HandBytes > HandCap:
		return "", u, fmt.Errorf("%s; %s", handCapReason(u.HandBytes, secs), trimHint)
	case u.Bytes > BriefingCap:
		return "", u, fmt.Errorf("the briefing is %s, over its %s cap (machine list %s, hand-written %s%s); %s", kb(u.Bytes), kb(BriefingCap), kb(len(gen)), kb(u.HandBytes), largest(secs), trimHint)
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

const trimHint = "nothing was rendered, and nothing was cut; move detail into a doc or a skill and leave a one-line pointer"

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
		if i == 0 || u.Bytes > best.Bytes || (best.Error == "" && u.Error != "") {
			best = u
		}
	}
	if len(keys) == 0 {
		best = BriefingUsage{Warn: BriefingWarn, Cap: BriefingCap, HandCap: HandCap}
	}
	return best
}
