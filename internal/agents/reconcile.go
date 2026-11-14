package agents

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Plan is how an agent, with the user, settles the sections gather found:
// for each, keep one version for every machine, keep each machine's own,
// merge them into new text, drop it, or leave it where it is.
type Plan struct {
	V           int           `json:"v"`
	Fingerprint string        `json:"fingerprint,omitempty"` // from gather --json: refuse if the files changed since
	Sections    []PlanSection `json:"sections"`
}

// PlanSection is one section's decision. Key is gather's section key.
type PlanSection struct {
	Key      string   `json:"key"`
	Action   string   `json:"action"`             // shared | per-machine | merge | drop | leave
	From     string   `json:"from,omitempty"`     // shared: a machine, or machine:~/path when it has two versions
	Text     string   `json:"text,omitempty"`     // merge: the merged text, heading included
	Machines []string `json:"machines,omitempty"` // per-machine: only these machines (default: all that have it)
}

// plan actions
const (
	ActShared     = "shared"
	ActPerMachine = "per-machine"
	ActMerge      = "merge"
	ActDrop       = "drop"
	ActLeave      = "leave"
)

// Trim is one file on one machine to take sections out of.
type Trim struct {
	Path   string   `json:"path"`   // under ~, like .claude/CLAUDE.md
	Hash   string   `json:"hash"`   // the whole file, as gather read it
	Remove []string `json:"remove"` // section keys
}

// Reconcile is what applying a plan does.
type Reconcile struct {
	Store map[string]string // store path -> its new text, only files that change
	Trims map[string][]Trim // machine -> its files to trim
	Lines []string          // one line per section, for people
}

// BuildReconcile checks plan against a fresh gather and works out the store
// files and the trims. current reads a store file as it is now. it writes
// nothing.
func BuildReconcile(r GatherReport, p Plan, current func(rel string) (string, bool)) (Reconcile, error) {
	out := Reconcile{Store: map[string]string{}, Trims: map[string][]Trim{}}
	if p.V != 1 {
		return out, fmt.Errorf("plan version %d; this yonder reads version 1 (\"v\": 1)", p.V)
	}
	if p.Fingerprint != "" && p.Fingerprint != r.Fingerprint {
		return out, errors.New("the machines' instructions files changed since the plan was made (its fingerprint doesn't match); run `yonder agents gather --json` again and check the plan against it")
	}
	byKey := map[string]GatherSection{}
	for _, s := range r.Sections {
		byKey[s.Key] = s
	}
	hashOf := map[string]string{} // machine:~/path -> hash
	for _, m := range r.Machines {
		for _, f := range m.Files {
			hashOf[m.Name+":"+f.Path] = f.Hash
		}
	}
	type keyText struct{ key, text string }
	var shared []keyText
	perMachine := map[string][]keyText{}
	trims := map[string]map[string][]string{} // machine -> path -> keys
	trim := func(file, key string) {
		m, path, _ := strings.Cut(file, ":")
		if trims[m] == nil {
			trims[m] = map[string][]string{}
		}
		if !slices.Contains(trims[m][path], key) {
			trims[m][path] = append(trims[m][path], key)
		}
	}
	seen := map[string]bool{}
	for _, ps := range p.Sections {
		sec, ok := byKey[ps.Key]
		if !ok {
			return out, fmt.Errorf("the plan names section %q, which gather didn't find; keys must match `yonder agents gather --json` exactly", ps.Key)
		}
		if seen[ps.Key] {
			return out, fmt.Errorf("section %q is in the plan twice", ps.Key)
		}
		seen[ps.Key] = true
		withHeading := func(body string) string {
			if sec.Heading == "" {
				return body
			}
			if body == "" {
				return sec.Heading
			}
			return sec.Heading + "\n" + body
		}
		allFiles := func() []string {
			var fs []string
			for _, v := range sec.Versions {
				fs = append(fs, v.Files...)
			}
			return fs
		}
		switch ps.Action {
		case ActLeave:
			out.Lines = append(out.Lines, fmt.Sprintf("%s: left as it is on each machine", ps.Key))
		case ActDrop:
			for _, f := range allFiles() {
				trim(f, ps.Key)
			}
			out.Lines = append(out.Lines, fmt.Sprintf("%s: dropped; taken out of %s", ps.Key, strings.Join(allFiles(), ", ")))
		case ActShared:
			v, err := pickVersion(sec, ps.From)
			if err != nil {
				return out, err
			}
			shared = append(shared, keyText{ps.Key, withHeading(v.Text)})
			for _, f := range allFiles() {
				trim(f, ps.Key)
			}
			out.Lines = append(out.Lines, fmt.Sprintf("%s: shared, %s's version, into instructions.md; taken out of %s", ps.Key, ps.From, strings.Join(allFiles(), ", ")))
		case ActMerge:
			t := normText(ps.Text)
			if t == "" {
				return out, fmt.Errorf("section %q: merge needs the merged text in \"text\"", ps.Key)
			}
			if strings.Contains(t, "<!-- yonder:") {
				return out, fmt.Errorf("section %q: the merged text holds yonder's marker, which would break the block it lands in", ps.Key)
			}
			shared = append(shared, keyText{ps.Key, t})
			for _, f := range allFiles() {
				trim(f, ps.Key)
			}
			out.Lines = append(out.Lines, fmt.Sprintf("%s: merged into instructions.md; taken out of %s", ps.Key, strings.Join(allFiles(), ", ")))
		case ActPerMachine:
			ms := ps.Machines
			if len(ms) == 0 {
				ms = sec.Machines
			}
			for _, m := range ms {
				var vs []GatherVersion
				for _, v := range sec.Versions {
					if slices.Contains(v.Machines, m) {
						vs = append(vs, v)
					}
				}
				switch len(vs) {
				case 0:
					return out, fmt.Errorf("section %q: %s doesn't have it", ps.Key, m)
				case 1:
				default:
					return out, fmt.Errorf("section %q: %s has %d versions of it (%s); merge them, or pick one with shared", ps.Key, m, len(vs), strings.Join(filesOn(vs, m), ", "))
				}
				perMachine[m] = append(perMachine[m], keyText{ps.Key, withHeading(vs[0].Text)})
				for _, f := range filesOn(vs, m) {
					trim(f, ps.Key)
				}
			}
			out.Lines = append(out.Lines, fmt.Sprintf("%s: kept per machine, in machines/<m>/instructions.md for %s", ps.Key, strings.Join(ms, ", ")))
		default:
			return out, fmt.Errorf("section %q: action %q; want shared, per-machine, merge, drop or leave", ps.Key, ps.Action)
		}
	}
	for _, s := range r.Sections {
		if !seen[s.Key] {
			out.Lines = append(out.Lines, fmt.Sprintf("%s: not in the plan, left as it is", s.Key))
		}
	}
	put := func(rel string, adds []keyText) {
		if len(adds) == 0 {
			return
		}
		cur, _ := current(rel)
		next := cur
		for _, a := range adds {
			next = upsertSection(next, a.key, a.text)
		}
		if next != cur {
			out.Store[rel] = next
		}
	}
	put("instructions.md", shared)
	for m, adds := range perMachine {
		put("machines/"+m+"/instructions.md", adds)
	}
	for m, files := range trims {
		for path, keys := range files {
			h, ok := hashOf[m+":"+path]
			if !ok {
				return out, fmt.Errorf("no hash for %s:%s in the gather", m, path)
			}
			out.Trims[m] = append(out.Trims[m], Trim{Path: strings.TrimPrefix(path, "~/"), Hash: h, Remove: keys})
		}
		slices.SortFunc(out.Trims[m], func(a, b Trim) int { return strings.Compare(a.Path, b.Path) })
	}
	return out, nil
}

func filesOn(vs []GatherVersion, m string) []string {
	var out []string
	for _, v := range vs {
		for _, f := range v.Files {
			if strings.HasPrefix(f, m+":") {
				out = append(out, f)
			}
		}
	}
	return out
}

// pickVersion is the version from names: a machine with one version of the
// section, or one file.
func pickVersion(sec GatherSection, from string) (GatherVersion, error) {
	if from == "" {
		return GatherVersion{}, fmt.Errorf("section %q: shared needs \"from\": the machine (or machine:~/path) whose version every machine gets", sec.Key)
	}
	var hits []GatherVersion
	for _, v := range sec.Versions {
		if slices.Contains(v.Files, from) || (!strings.Contains(from, ":") && slices.Contains(v.Machines, from)) {
			hits = append(hits, v)
		}
	}
	switch len(hits) {
	case 0:
		return GatherVersion{}, fmt.Errorf("section %q: %s has no version of it", sec.Key, from)
	case 1:
		return hits[0], nil
	}
	return GatherVersion{}, fmt.Errorf("section %q: %s has %d versions of it (%s); name one file as \"from\": \"%s\"", sec.Key, from, len(hits), strings.Join(filesOn(hits, from), ", "), filesOn(hits, from)[0])
}

// upsertSection puts text in place of the section with this key in cur, or
// appends it.
func upsertSection(cur, key, text string) string {
	lines := strings.Split(cur, "\n")
	for _, s := range splitSections(cur) {
		if s.key != key {
			continue
		}
		first, last := s.lines[0], s.lines[len(s.lines)-1]
		// keep the blank lines that ended the old section
		for last > first && strings.TrimSpace(lines[last]) == "" {
			last--
		}
		next := append([]string{}, lines[:first]...)
		next = append(next, strings.Split(text, "\n")...)
		next = append(next, lines[last+1:]...)
		return strings.Join(next, "\n")
	}
	cur = strings.TrimRight(cur, "\n")
	if cur == "" {
		return text + "\n"
	}
	return cur + "\n\n" + text + "\n"
}

// TrimSections takes the sections with these keys out of an instructions
// file, leaving yonder's block and the rest of the user's text as they are.
// it returns the new text and the keys it found.
func TrimSections(data string, keys []string) (string, []string, error) {
	if strings.HasPrefix(data, renderMark) {
		return data, nil, nil // all yonder's: nothing of the user's here
	}
	before, after, found, err := splitBlock(data)
	if err != nil {
		return "", nil, err
	}
	block := ""
	if found {
		block = data[len(before) : len(data)-len(after)]
	} else {
		before, after = data, ""
	}
	own := before + after
	nb := strings.Count(before, "\n") // lines of own that come before the block
	lines := strings.Split(own, "\n")
	drop := map[int]bool{}
	var removed []string
	for _, s := range splitSections(own) {
		if slices.Contains(keys, s.key) {
			for _, i := range s.lines {
				drop[i] = true
			}
			removed = append(removed, s.key)
		}
	}
	if len(removed) == 0 {
		return data, nil, nil
	}
	keep := func(from, to int) []string {
		var out []string
		blank := false
		for i := from; i < to; i++ {
			if drop[i] {
				continue
			}
			b := strings.TrimSpace(lines[i]) == ""
			if b && blank {
				continue // a cut leaves one blank line, not a run
			}
			blank = b
			out = append(out, lines[i])
		}
		return out
	}
	var b strings.Builder
	head := keep(0, nb)
	for len(head) > 0 && strings.TrimSpace(head[0]) == "" {
		head = head[1:]
	}
	for _, l := range head {
		b.WriteString(l + "\n")
	}
	b.WriteString(block)
	tail := keep(nb, len(lines))
	b.WriteString(strings.Join(tail, "\n"))
	return b.String(), removed, nil
}
