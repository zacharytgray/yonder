package agents

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// GatherFile is one user instructions file gather reads, under ~. hermes's
// SOUL.md is left out: it is the agent's persona, not rules to share.
type GatherFile struct {
	Harness string
	Path    string
}

var GatherFiles = []GatherFile{
	{"claude-code", ".claude/CLAUDE.md"},
	{"codex", ".codex/AGENTS.md"},
	{"pi", ".pi/agent/AGENTS.md"},
}

const gatherMax = 256 << 10 // a bigger file isn't instructions anyone wrote by hand

// ReplyStart opens a read-only script's reply: a login shell's profile may
// print first, and the parsers skip to it.
const ReplyStart = "=== yonder reply"

// GatherScript prints each file that exists as "=== <path> <bytes>", its
// bytes, then a newline. it only reads.
func GatherScript() string {
	var paths []string
	for _, f := range GatherFiles {
		paths = append(paths, f.Path)
	}
	return `printf '%s\n' '` + ReplyStart + `'
for f in ` + strings.Join(paths, " ") + `; do
  p="$HOME/$f"
  [ -f "$p" ] || continue
  n=$(wc -c < "$p" | tr -d ' ')
  if [ "$n" -gt ` + strconv.Itoa(gatherMax) + ` ]; then printf '=== %s %s skipped\n' "$f" "$n"; continue; fi
  printf '=== %s %s\n' "$f" "$n"; cat "$p"; printf '\n'
done
`
}

// ParseGather reads GatherScript's output: path -> content.
func ParseGather(r io.Reader) (map[string][]byte, error) {
	out := map[string][]byte{}
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, errors.New("no reply from the gather script")
		}
		if strings.TrimSuffix(line, "\n") == ReplyStart {
			break
		}
	}
	for {
		line, err := br.ReadString('\n')
		if errors.Is(err, io.EOF) && line == "" {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("short reply: %w", err)
		}
		f := strings.Fields(strings.TrimSuffix(line, "\n"))
		if len(f) < 3 || f[0] != "===" {
			return nil, fmt.Errorf("unexpected line %q", strings.TrimSpace(line))
		}
		if len(f) == 4 && f[3] == "skipped" {
			continue
		}
		n, err := strconv.Atoi(f[2])
		if err != nil || n < 0 || n > gatherMax {
			return nil, fmt.Errorf("bad size in %q", strings.TrimSpace(line))
		}
		buf := make([]byte, n+1)
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, fmt.Errorf("short reply for %s: %w", f[1], err)
		}
		out[f[1]] = buf[:n]
	}
}

// GatherHere reads this machine's files directly.
func GatherHere(home string) map[string][]byte {
	out := map[string][]byte{}
	for _, f := range GatherFiles {
		p := filepath.Join(home, f.Path)
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() || st.Size() > gatherMax {
			continue
		}
		if data, err := os.ReadFile(p); err == nil {
			out[f.Path] = data
		}
	}
	return out
}

// OwnText is the user's own text in an instructions file: yonder's block
// taken out, and nothing for a file yonder rendered whole.
func OwnText(data []byte) (string, bool) {
	text := string(data)
	if strings.HasPrefix(text, renderMark) {
		return "", true
	}
	before, after, found, err := splitBlock(text)
	if err != nil {
		return text, false
	}
	if found {
		return before + after, true
	}
	return text, false
}

// a section of one file: a heading and what follows it up to the next heading,
// or, where there are no headings, a paragraph
type section struct {
	key, heading, text string
}

func splitSections(text string) []section {
	var out []section
	var cur *section
	var para []string
	keys := map[string]int{}
	addKey := func(k string) string {
		keys[k]++
		if keys[k] > 1 {
			return fmt.Sprintf("%s #%d", k, keys[k])
		}
		return k
	}
	flushPara := func() {
		t := normText(strings.Join(para, "\n"))
		para = nil
		if t != "" {
			out = append(out, section{key: addKey("¶ " + clip(firstLineOf(t), 60)), text: t})
		}
	}
	flushHead := func() {
		if cur != nil {
			cur.text = normText(cur.text)
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if isHeading(line) {
			flushPara()
			flushHead()
			h := strings.TrimSpace(line)
			cur = &section{key: addKey(strings.ToLower(strings.Join(strings.Fields(h), " "))), heading: h}
			continue
		}
		if cur != nil {
			cur.text += line + "\n"
			continue
		}
		if strings.TrimSpace(line) == "" {
			flushPara()
			continue
		}
		para = append(para, line)
	}
	flushPara()
	flushHead()
	return out
}

func firstLineOf(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// normText: trailing spaces off each line, runs of blank lines as one, trimmed.
func normText(s string) string {
	var out []string
	blank := false
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if l == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// MachineFiles is what gather read on one machine.
type MachineFiles struct {
	Machine string
	Files   map[string][]byte // path under ~ -> content
	Err     string            // why it wasn't read (offline, an error)
}

// GatherReport says which sections are the same everywhere, which differ,
// and which only some machines have. it never merges anything.
type GatherReport struct {
	Machines []GatherMachine `json:"machines"`
	Sections []GatherSection `json:"sections"`
	Summary  map[string]int  `json:"summary"` // same, differs, some, only
}

type GatherMachine struct {
	Name  string           `json:"name"`
	Files []GatherFileInfo `json:"files"`
	Error string           `json:"error,omitempty"`
}

type GatherFileInfo struct {
	Path     string `json:"path"` // ~/...
	Harness  string `json:"harness"`
	Bytes    int    `json:"bytes"`  // the user's own text
	Yonder   bool   `json:"yonder"` // held a yonder block, or was rendered by yonder; that part is left out
	Rendered bool   `json:"rendered,omitempty"`
}

type GatherSection struct {
	Key      string          `json:"key"`
	Heading  string          `json:"heading"` // "" for a paragraph
	Status   string          `json:"status"`  // same, differs, some, only
	Machines []string        `json:"machines"`
	Versions []GatherVersion `json:"versions"`
}

type GatherVersion struct {
	Machines []string `json:"machines"`
	Files    []string `json:"files"` // machine:~/path
	Text     string   `json:"text"`
}

// section statuses
const (
	SameEverywhere = "same"
	Differs        = "differs"
	OnSome         = "some"
	OnOne          = "only"
)

// Compare sorts every section of every machine's files. machines with no
// files are listed but don't count toward "everywhere".
func Compare(ms []MachineFiles) GatherReport {
	r := GatherReport{Machines: []GatherMachine{}, Sections: []GatherSection{}, Summary: map[string]int{}}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Machine < ms[j].Machine })
	byKey := map[string]*GatherSection{}
	var order []string
	var with []string // machines that have files
	for _, m := range ms {
		gm := GatherMachine{Name: m.Machine, Files: []GatherFileInfo{}, Error: m.Err}
		any := false
		for _, gf := range GatherFiles {
			data, ok := m.Files[gf.Path]
			if !ok {
				continue
			}
			own, yonder := OwnText(data)
			gm.Files = append(gm.Files, GatherFileInfo{Path: "~/" + gf.Path, Harness: gf.Harness, Bytes: len(strings.TrimSpace(own)),
				Yonder: yonder, Rendered: yonder && own == ""})
			for _, s := range splitSections(own) {
				any = true
				gs, ok := byKey[s.key]
				if !ok {
					gs = &GatherSection{Key: s.key, Heading: s.heading}
					byKey[s.key] = gs
					order = append(order, s.key)
				}
				addVersion(gs, m.Machine, m.Machine+":~/"+gf.Path, s.text)
			}
		}
		if any {
			with = append(with, m.Machine)
		}
		r.Machines = append(r.Machines, gm)
	}
	for _, k := range order {
		gs := byKey[k]
		switch {
		case len(gs.Versions) > 1:
			gs.Status = Differs
		case len(with) > 1 && len(gs.Machines) == len(with):
			gs.Status = SameEverywhere
		case len(gs.Machines) == 1:
			gs.Status = OnOne
		default:
			gs.Status = OnSome
		}
		r.Summary[gs.Status]++
		r.Sections = append(r.Sections, *gs)
	}
	return r
}

func addVersion(gs *GatherSection, machine, file, text string) {
	if !contains(gs.Machines, machine) {
		gs.Machines = append(gs.Machines, machine)
	}
	for i := range gs.Versions {
		v := &gs.Versions[i]
		if v.Text == text {
			if !contains(v.Machines, machine) {
				v.Machines = append(v.Machines, machine)
			}
			v.Files = append(v.Files, file)
			return
		}
	}
	gs.Versions = append(gs.Versions, GatherVersion{Machines: []string{machine}, Files: []string{file}, Text: text})
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Diverged reports whether two or more machines have instructions and they
// don't all agree: what init and enable warn about.
func (r GatherReport) Diverged() bool {
	n := 0
	for _, m := range r.Machines {
		for _, f := range m.Files {
			if f.Bytes > 0 {
				n++
				break
			}
		}
	}
	return n > 1 && r.Summary[Differs]+r.Summary[OnSome]+r.Summary[OnOne] > 0
}
