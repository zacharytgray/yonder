package agents

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
)

// Finding is one thing to fix or trim in the briefing.
type Finding struct {
	Kind   string `json:"kind"`           // budget, path, machine, contradiction, review, duplicate
	Line   int    `json:"line,omitempty"` // in briefing.md; 0 when it isn't about one line
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
	Cut    string `json:"cut,omitempty"` // the suggested draft takes this text out of the line
}

// PathRef is a path the hand-written briefing names, to check on its machine.
type PathRef struct {
	Machine string `json:"machine"` // "" for every machine (a ~/ path)
	Path    string `json:"path"`    // ~/... or absolute, cut before any placeholder
	Written string `json:"written"` // as it appears in briefing.md
	Line    int    `json:"line"`
}

var (
	codeSpan    = regexp.MustCompile("`([^`\n]+)`")
	machinePath = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*):((?:~/|/)\S*)$`)
	yonderArg   = regexp.MustCompile(`\byonder\s+(?:run|show|jobs|set|refresh|add|mesh|sync)\s+(?:--?[a-z-]+\s+)*([a-z0-9][a-z0-9-]*)\b`)
	ipv4        = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	roles       = `laptop|desktop|server|gpu`
)

// BriefingPaths lists the paths in briefing.md's code spans: `<machine>:<path>`
// for one machine, `~/...` for every machine. a path is cut before its first
// placeholder (`~/repos/<repo>` checks ~/repos). spans with spaces are
// commands, not paths.
func BriefingPaths(data []byte, ms []registry.Machine) []PathRef {
	known := names(ms)
	var out []PathRef
	for i, line := range strings.Split(string(data), "\n") {
		for _, m := range codeSpan.FindAllStringSubmatch(line, -1) {
			span := m[1]
			ref := PathRef{Written: span, Line: i + 1}
			if mm := machinePath.FindStringSubmatch(span); mm != nil {
				if !known[mm[1]] {
					continue // an unknown machine is its own finding
				}
				ref.Machine, ref.Path = mm[1], mm[2]
			} else if strings.HasPrefix(span, "~/") && !strings.ContainsAny(span, " \t") {
				ref.Path = span
			} else {
				continue
			}
			if ref.Path = cutPlaceholder(ref.Path); ref.Path == "" {
				continue
			}
			out = append(out, ref)
		}
	}
	return out
}

func cutPlaceholder(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if strings.ContainsAny(part, "<>{}*?[]$|") {
			parts = parts[:i]
			break
		}
	}
	p = strings.TrimRight(strings.Join(parts, "/"), "/")
	if p == "" || p == "~" {
		return ""
	}
	return p
}

func names(ms []registry.Machine) map[string]bool {
	out := map[string]bool{}
	for _, m := range ms {
		out[m.Name] = true
	}
	return out
}

// CheckBriefing finds what doesn't fit the records in briefing.md: unknown or
// removed machine names, roles and addresses that contradict a record, and
// sections not reviewed in 90 days. paths are checked by the caller, on their
// machines (BriefingPaths).
func CheckBriefing(data []byte, ms []registry.Machine, removed []string, now time.Time) []Finding {
	var out []Finding
	known := names(ms)
	gone := map[string]bool{}
	for _, n := range removed {
		gone[n] = true
	}
	byIP := map[string]registry.Machine{}
	for _, m := range ms {
		if m.TailscaleIP != "" {
			byIP[m.TailscaleIP] = m
		}
	}
	unknown := func(line int, n string) {
		detail := fmt.Sprintf("line %d names %s, which is not a fleet machine", line, n)
		if gone[n] {
			detail = fmt.Sprintf("line %d names %s, which was removed", line, n)
		}
		out = append(out, Finding{Kind: "machine", Line: line, Detail: detail, Fix: "fix the name (yonder ls lists them), or drop the line"})
	}
	for i, line := range strings.Split(string(data), "\n") {
		n := i + 1
		seen := map[string]bool{}
		for _, m := range codeSpan.FindAllStringSubmatch(line, -1) {
			if mm := machinePath.FindStringSubmatch(m[1]); mm != nil && !known[mm[1]] && !seen[mm[1]] {
				seen[mm[1]] = true
				unknown(n, mm[1])
			} else if gone[m[1]] && !seen[m[1]] {
				seen[m[1]] = true
				unknown(n, m[1])
			}
		}
		for _, m := range yonderArg.FindAllStringSubmatch(line, -1) {
			if !known[m[1]] && !seen[m[1]] {
				seen[m[1]] = true
				unknown(n, m[1])
			}
		}
		named := namedOn(line, ms)
		for _, ip := range ipv4.FindAllString(line, -1) {
			owner, ok := byIP[ip]
			switch {
			case !ok:
				out = append(out, Finding{Kind: "contradiction", Line: n,
					Detail: fmt.Sprintf("line %d names the address %s, which no machine has", n, ip),
					Fix:    "addresses come from the records and are listed above it; drop it"})
			case len(named) > 0 && !named[owner.Name]:
				out = append(out, Finding{Kind: "contradiction", Line: n,
					Detail: fmt.Sprintf("line %d puts %s, which is %s's address, next to another machine", n, ip, owner.Name),
					Fix:    "addresses come from the records and are listed above it; drop it"})
			default:
				out = append(out, Finding{Kind: "contradiction", Line: n, Cut: ip,
					Detail: fmt.Sprintf("line %d repeats %s's address, which the machine list already has", n, owner.Name),
					Fix:    "drop it; hand-written text never repeats generated facts"})
			}
		}
		for _, m := range ms {
			if !named[m.Name] {
				continue
			}
			if r := claimedRole(line, m.Name); r != "" && r != m.Role && !known[r] {
				fix := fmt.Sprintf("fix the line, or `yonder set %s --role %s` if it is right", m.Name, r)
				detail := fmt.Sprintf("line %d calls %s the %s, but its record says %s", n, m.Name, r, orStr(m.Role, "no role"))
				out = append(out, Finding{Kind: "contradiction", Line: n, Detail: detail, Fix: fix})
			}
		}
	}
	gen := map[string]bool{}
	for _, line := range strings.Split(GeneratedBriefing(ms, ""), "\n") {
		if n := normLine(line); len(n) >= minDupLen {
			gen[n] = true
		}
	}
	for i, line := range strings.Split(string(data), "\n") {
		if gen[normLine(line)] {
			out = append(out, Finding{Kind: "duplicate", Line: i + 1, Cut: strings.TrimSpace(line),
				Detail: fmt.Sprintf("line %d repeats the generated machine list", i+1),
				Fix:    "drop it; hand-written text never repeats generated facts"})
		}
	}
	secs, _ := parseBriefing(data)
	for _, s := range secs {
		what := "the text at the top"
		if s.Heading != "" {
			what = fmt.Sprintf("section %q", s.Heading)
		}
		switch {
		case s.Reviewed.IsZero():
			out = append(out, Finding{Kind: "review", Line: s.Line, Detail: what + " has no reviewed: date",
				Fix: "check it, then add a line `reviewed: " + now.Format(time.DateOnly) + "` under its heading (or one at the top for the whole file)"})
		case now.Sub(s.Reviewed) > ReviewMaxAge:
			out = append(out, Finding{Kind: "review", Line: s.Line,
				Detail: fmt.Sprintf("%s was last reviewed %d days ago", what, int(now.Sub(s.Reviewed).Hours()/24)),
				Fix:    "check it is still true, then set its reviewed: line to today"})
		}
	}
	return out
}

// namedOn is the machines a line names as whole words.
func namedOn(line string, ms []registry.Machine) map[string]bool {
	out := map[string]bool{}
	for _, m := range ms {
		if regexp.MustCompile(`(^|[^a-z0-9-])` + regexp.QuoteMeta(m.Name) + `($|[^a-z0-9-])`).MatchString(line) {
			out[m.Name] = true
		}
	}
	return out
}

// claimedRole: "gpu-box is the server", "`gpu-box` (server)", "the server, gpu-box".
func claimedRole(line, name string) string {
	q := regexp.QuoteMeta(name)
	for _, re := range []string{
		"`?" + q + "`?\\s+(?:is|as|=)\\s+(?:the|a|an|my|our)?\\s*(" + roles + ")\\b",
		"`?" + q + "`?\\s*\\((" + roles + ")\\)",
		"\\b(?:the|my|our)\\s+(" + roles + ")(?:\\s+(?:box|machine))?\\s*[,:(]?\\s*`?" + q + "\\b",
	} {
		if m := regexp.MustCompile(`(?i)` + re).FindStringSubmatch(line); m != nil {
			return strings.ToLower(m[1])
		}
	}
	return ""
}

// normLine is a line compared for duplicates: lower case, no list marker,
// no markdown punctuation at the ends, single spaces.
func normLine(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimLeft(s, "-*+> ")
	s = strings.Trim(s, " .:;,*_`")
	return strings.Join(strings.Fields(s), " ")
}

const minDupLen = 24 // shorter lines repeat by chance

// Duplicates finds the briefing's lines that the user's own text in each
// linked instructions file repeats, outside yonder's block. the briefing loads
// in that file anyway, so the user's copy can go.
func (l *Linker) Duplicates() []Finding {
	st, err := l.State()
	if err != nil || st.Paused {
		return nil
	}
	data, _ := l.briefingSource()
	_, hand := parseBriefing(data)
	handLines := map[string]int{}
	srcLines := strings.Split(string(data), "\n")
	for i, line := range srcLines {
		if n := normLine(line); len(n) >= minDupLen {
			handLines[n] = i + 1
		}
	}
	gen := GeneratedBriefing(l.machines(), l.Self)
	brief := map[string]bool{}
	for _, line := range strings.Split(gen+"\n"+hand, "\n") {
		if n := normLine(line); len(n) >= minDupLen && !isHeading(strings.TrimSpace(line)) {
			brief[n] = true
		}
	}
	var out []Finding
	for _, e := range st.Entries {
		if e.Kind != kindBlock {
			continue
		}
		text, err := os.ReadFile(e.Path)
		if err != nil {
			continue
		}
		before, after, found, err := splitBlock(string(text))
		if err != nil || !found {
			continue
		}
		for _, line := range strings.Split(before+"\n"+after, "\n") {
			n := normLine(line)
			if !brief[n] {
				continue
			}
			where := "the machine list"
			if ln, ok := handLines[n]; ok {
				where = fmt.Sprintf("line %d of agents/%s", ln, BriefingFile)
			}
			out = append(out, Finding{Kind: "duplicate",
				Detail: fmt.Sprintf("%s repeats %s outside yonder's block: %q", l.tilde(e.Path), where, clip(strings.TrimSpace(line), 80)),
				Fix:    "delete it from your own text in " + l.tilde(e.Path) + "; the briefing loads there anyway"})
		}
	}
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// BriefingReview is what doctor and `agents briefing review` show.
type BriefingReview struct {
	Usage    BriefingUsage `json:"usage"`
	Findings []Finding     `json:"findings"`
	Paths    []PathRef     `json:"paths"` // still to check on their machines
	Source   string        `json:"source"`
	Rendered string        `json:"rendered"`
}

// ReviewBriefing gathers the budget, the findings that need no other
// machine, and the paths to check.
func (l *Linker) ReviewBriefing(now time.Time) BriefingReview {
	r := BriefingReview{Usage: l.BriefingUsage(), Findings: []Finding{}, Paths: []PathRef{}}
	data, ok := l.briefingSource()
	r.Source = string(data)
	if text, _, err := l.briefing("claude"); err == nil {
		r.Rendered = text
	}
	switch {
	case r.Usage.Error != "":
		r.Findings = append(r.Findings, Finding{Kind: "budget", Detail: r.Usage.Error, Fix: "trim agents/" + BriefingFile})
	case r.Usage.Bytes > BriefingWarn:
		r.Findings = append(r.Findings, Finding{Kind: "budget", Detail: r.Usage.String() + ": past the warning line",
			Fix: "move detail into a doc or a skill and leave a one-line pointer"})
	}
	if !ok {
		return r
	}
	ms := l.machines()
	var removed []string
	if l.Registry != nil {
		ts, _ := l.Registry.Tombstones()
		for _, t := range ts {
			removed = append(removed, t.Name)
		}
	}
	r.Findings = append(r.Findings, CheckBriefing(data, ms, removed, now)...)
	r.Findings = append(r.Findings, l.Duplicates()...)
	r.Paths = append(r.Paths, BriefingPaths(data, ms)...)
	return r
}

var emptyParens = regexp.MustCompile(`\s*\(\s*\)|\s*\[\s*\]`)

// Draft is briefing.md with what the findings say to cut taken out, and a
// line left empty by that dropped. it is a suggestion to edit; yonder never
// writes it.
func Draft(source string, fs []Finding) string {
	cuts := map[int][]string{}
	for _, f := range fs {
		if f.Cut != "" && f.Line > 0 {
			cuts[f.Line] = append(cuts[f.Line], f.Cut)
		}
	}
	var out []string
	for i, line := range strings.Split(source, "\n") {
		cs, ok := cuts[i+1]
		if !ok {
			out = append(out, line)
			continue
		}
		for _, c := range cs {
			if c == strings.TrimSpace(line) {
				line = ""
				break
			}
			line = regexp.MustCompile(`(?:\s+(?:at|on|is|via))?\s*`+"`?"+regexp.QuoteMeta(c)+"`?").ReplaceAllString(line, "")
		}
		line = emptyParens.ReplaceAllString(line, "")
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		line = indent + strings.Join(strings.Fields(line), " ")
		line = strings.ReplaceAll(strings.TrimRight(strings.TrimSuffix(strings.TrimRight(line, " "), " ·"), " ,;"), " .", ".")
		if strings.Trim(line, " -*·") == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
