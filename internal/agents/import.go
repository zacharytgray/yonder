package agents

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/zacharytgray/yonder/internal/store"
)

// kinds of agent config import takes. nothing else goes in: no settings,
// auth or credential files.
const (
	KindInstructions = "instructions"
	KindSkills       = "skills"
	KindSubagents    = "subagents"
	KindCommands     = "commands"
	KindHooks        = "hooks"
	KindFiles        = "files"
)

// Kinds lists the import kinds.
var Kinds = []string{KindInstructions, KindSkills, KindSubagents, KindCommands, KindHooks, KindFiles}

// instruction file names import recognises in a folder, first one wins
var instructionNames = []string{"instructions.md", "CONTEXT.md", "AGENTS.md", "CLAUDE.md"}

// ImportOptions says what to take from where.
type ImportOptions struct {
	Src     string
	Kinds   []string // empty: whatever the source holds
	Harness string   // instructions go to that harness's overlay; files need it
	Machine string   // put everything under machines/<name>/
	Replace bool     // overwrite store files that differ
	DryRun  bool
}

// ImportFile is one file import would put in the store.
type ImportFile struct {
	Src  string
	Rel  string
	Data []byte
	Exec bool
}

// ImportPlan is what import found, and what it leaves out and why.
type ImportPlan struct {
	Files   []ImportFile
	Skipped []string
}

// PlanImport walks the source and maps it onto the store layout.
func PlanImport(o ImportOptions) (ImportPlan, error) {
	var p ImportPlan
	for _, k := range o.Kinds {
		if !slices.Contains(Kinds, k) {
			return p, fmt.Errorf("unknown kind %q (kinds: %s)", k, strings.Join(Kinds, ", "))
		}
	}
	key := ""
	if o.Harness != "" {
		h, ok := Lookup(o.Harness)
		if !ok {
			return p, fmt.Errorf("unknown harness %q (want one of %s)", o.Harness, strings.Join(Names(), ", "))
		}
		key = wirings[h.Name].key
	}
	want := func(k string) bool { return len(o.Kinds) == 0 || slices.Contains(o.Kinds, k) }
	prefix := ""
	if o.Machine != "" {
		prefix = "machines/" + o.Machine + "/"
	}
	instrRel := "instructions.md"
	if key != "" {
		instrRel = "instructions." + key + ".md"
	}
	src, err := filepath.Abs(o.Src)
	if err != nil {
		return p, err
	}
	info, err := os.Stat(src)
	if err != nil {
		return p, err
	}
	base := filepath.Base(src)

	if !info.IsDir() {
		kind := ""
		switch {
		case len(o.Kinds) == 1:
			kind = o.Kinds[0]
		case len(o.Kinds) > 1:
			return p, errors.New("a single file takes one --kind")
		case slices.Contains(instructionNames, base):
			kind = KindInstructions
		default:
			return p, fmt.Errorf("%s: say what it is with --kind (%s)", o.Src, strings.Join(Kinds, ", "))
		}
		var rel string
		switch kind {
		case KindInstructions:
			rel = instrRel
		case KindSubagents:
			rel = "claude/agents/" + base
		case KindCommands:
			rel = "claude/commands/" + base
		case KindHooks:
			rel = "hooks/" + base
		case KindFiles:
			if key == "" {
				return p, errors.New("--kind files needs --harness, the harness the file belongs to")
			}
			rel = "files/" + key + "/" + base
		case KindSkills:
			return p, errors.New("a skill is a folder with a SKILL.md; import the folder")
		}
		return p, p.addFile(src, prefix+rel)
	}

	// one skill folder
	if fileExists(filepath.Join(src, "SKILL.md")) && want(KindSkills) {
		return p, p.addSkill(src, prefix+"skills/"+base)
	}
	// a folder of one kind
	if len(o.Kinds) == 1 && o.Kinds[0] != KindInstructions {
		return p, p.addKindDir(src, o.Kinds[0], prefix, key)
	}
	// a harness or config folder: take each kind it holds
	if want(KindInstructions) {
		for _, n := range instructionNames {
			if f := filepath.Join(src, n); fileExists(f) {
				if err := p.addFile(f, prefix+instrRel); err != nil {
					return p, err
				}
				break
			}
		}
	}
	for _, d := range []struct{ dir, kind string }{
		{"skills", KindSkills}, {"agents", KindSubagents}, {"commands", KindCommands}, {"hooks", KindHooks},
	} {
		if dir := filepath.Join(src, d.dir); want(d.kind) && dirExists(dir) {
			if err := p.addKindDir(dir, d.kind, prefix, key); err != nil {
				return p, err
			}
		}
	}
	if len(p.Files) == 0 && len(p.Skipped) == 0 {
		return p, fmt.Errorf("found nothing to import in %s (it looks for %s, skills/, agents/, commands/ and hooks/)", o.Src, strings.Join(instructionNames, ", "))
	}
	return p, nil
}

func (p *ImportPlan) addKindDir(dir, kind, prefix, key string) error {
	es, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range es {
		full := filepath.Join(dir, e.Name())
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		switch kind {
		case KindSkills:
			if why, ok := reservedSkills[e.Name()]; ok {
				p.Skipped = append(p.Skipped, full+": "+why)
				continue
			}
			if !dirExists(full) || !fileExists(filepath.Join(full, "SKILL.md")) {
				p.Skipped = append(p.Skipped, full+": not a skill folder (no SKILL.md)")
				continue
			}
			if err := p.addSkill(full, prefix+"skills/"+e.Name()); err != nil {
				return err
			}
		case KindSubagents, KindCommands, KindHooks, KindFiles:
			if !fileExists(full) {
				p.Skipped = append(p.Skipped, full+": not a file")
				continue
			}
			rel := map[string]string{KindSubagents: "claude/agents/", KindCommands: "claude/commands/", KindHooks: "hooks/"}[kind]
			if kind == KindFiles {
				if key == "" {
					return errors.New("--kind files needs --harness, the harness the files belong to")
				}
				rel = "files/" + key + "/"
			}
			if err := p.addFile(full, prefix+rel+e.Name()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("--kind %s takes a file", kind)
		}
	}
	return nil
}

func (p *ImportPlan) addSkill(dir, rel string) error {
	if why, ok := reservedSkills[path.Base(rel)]; ok {
		p.Skipped = append(p.Skipped, dir+": "+why)
		return nil
	}
	return filepath.WalkDir(dir, func(f string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		sub, _ := filepath.Rel(dir, f)
		return p.addFile(f, rel+"/"+filepath.ToSlash(sub))
	})
}

// addFile reads one file, following a symlink to a regular file.
func (p *ImportPlan) addFile(src, rel string) error {
	if path.Base(rel) == ".DS_Store" {
		return nil
	}
	info, err := os.Stat(src)
	if err != nil {
		p.Skipped = append(p.Skipped, src+": "+err.Error())
		return nil
	}
	if !info.Mode().IsRegular() {
		p.Skipped = append(p.Skipped, src+": not a regular file")
		return nil
	}
	if info.Size() > store.MaxFile {
		p.Skipped = append(p.Skipped, fmt.Sprintf("%s: %d bytes, over the %d MiB limit", src, info.Size(), store.MaxFile>>20))
		return nil
	}
	if err := store.ValidRel(rel); err != nil {
		p.Skipped = append(p.Skipped, src+": "+err.Error())
		return nil
	}
	if parts := layout(rel); len(parts) >= 3 && parts[0] == "files" && HarnessFile(parts[len(parts)-1]) {
		p.Skipped = append(p.Skipped, src+": a harness's own settings, auth or instructions file; those never go in the store")
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if isInstructions(rel) {
		text := string(data)
		if strings.HasPrefix(text, renderMark) {
			p.Skipped = append(p.Skipped, src+": yonder rendered it from the store; nothing to import")
			return nil
		}
		// an instructions file already linked here holds yonder's block: take the user's text only
		mine, err := withoutBlock(text)
		if err != nil {
			p.Skipped = append(p.Skipped, src+": "+err.Error())
			return nil
		}
		if strings.Contains(mine, markerPrefix) {
			p.Skipped = append(p.Skipped, src+": it holds a yonder marker outside a block; take it out first")
			return nil
		}
		data = []byte(mine)
	}
	p.Files = append(p.Files, ImportFile{Src: src, Rel: rel, Data: data, Exec: info.Mode()&0o111 != 0})
	return nil
}

// ImportResult is what one file did.
type ImportResult struct {
	Rel     string
	Action  string // added | replaced | unchanged | exists | refused
	Detail  string
	Refused bool
}

// Import puts the plan's files into the store. callers hold the lock and
// scan afterwards, which records each one as a change on this machine.
func Import(s *store.Store, p ImportPlan, replace, dry bool) []ImportResult {
	var out []ImportResult
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Rel < p.Files[j].Rel })
	for _, f := range p.Files {
		r := ImportResult{Rel: f.Rel}
		if ref := Check(f.Rel, f.Data); ref != nil {
			r.Action, r.Detail, r.Refused = "refused", ref.String(), true
			out = append(out, r)
			continue
		}
		cur, exists := s.Live(f.Rel)
		switch {
		case exists && bytes.Equal(cur, f.Data):
			r.Action = "unchanged"
			out = append(out, r)
			continue
		case exists && !replace:
			r.Action, r.Detail = "exists", "differs from "+f.Src+"; --replace overwrites it (the old one is kept)"
			out = append(out, r)
			continue
		case exists:
			r.Action = "replaced"
		default:
			r.Action = "added"
		}
		if !dry {
			if err := s.Put(f.Rel, f.Data, f.Exec); err != nil {
				r.Action, r.Detail, r.Refused = "failed", err.Error(), true
			}
		}
		out = append(out, r)
	}
	return out
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
