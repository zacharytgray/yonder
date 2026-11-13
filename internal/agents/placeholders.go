package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// RenderFile names each harness's own model tiers. text in the store says
// {{model:<key>}} and each harness gets its own name for it:
//
//	[claude]
//	top = "Atlas"
//	[codex]
//	top = "Zephyr"
const RenderFile = "render.toml"

var (
	placeholder = regexp.MustCompile(`\{\{model:([a-z0-9][a-z0-9-]*)\}\}`)
	tierKey     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// harness keys a table may name
var renderKeys = []string{"claude", "codex", "pi", "hermes"}

// renderTable is render.toml: harness key -> tier -> model name.
type renderTable map[string]map[string]string

// parseRenderTable reads and checks render.toml. only tables named after a
// harness, holding one-line names, are allowed.
func parseRenderTable(data []byte) (renderTable, error) {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("agents/%s is not valid TOML: %w", RenderFile, err)
	}
	t := renderTable{}
	for h, v := range raw {
		if !contains(renderKeys, h) {
			return nil, fmt.Errorf("agents/%s has [%s]; the tables are %s", RenderFile, h, strings.Join(renderKeys, ", "))
		}
		tab, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("agents/%s: %s must be a table, like [%s]", RenderFile, h, h)
		}
		t[h] = map[string]string{}
		for k, val := range tab {
			s, ok := val.(string)
			switch {
			case !tierKey.MatchString(k):
				return nil, fmt.Errorf("agents/%s: [%s] %s is not a tier name (a-z, 0-9, -)", RenderFile, h, k)
			case !ok:
				return nil, fmt.Errorf("agents/%s: [%s] %s must be a string", RenderFile, h, k)
			case s == "" || len(s) > 100 || strings.ContainsAny(s, "\n\r") || strings.Contains(s, "{{") || strings.Contains(s, markerPrefix):
				return nil, fmt.Errorf("agents/%s: [%s] %s must be one short line of plain text", RenderFile, h, k)
			}
			t[h][k] = s
		}
	}
	return t, nil
}

// renderTable is the store's table; none when there is no render.toml.
func (l *Linker) renderTable() (renderTable, error) {
	data, err := os.ReadFile(filepath.Join(l.StoreDir, RenderFile))
	if err != nil {
		return renderTable{}, nil
	}
	return parseRenderTable(data)
}

// Placeholders lists the tiers a text uses, in order, once each.
func Placeholders(text string) []string {
	var out []string
	for _, m := range placeholder.FindAllStringSubmatch(text, -1) {
		if !contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// resolve replaces {{model:<tier>}} with the harness's name for it. only that
// exact syntax is touched. call tierError first: a tier with no name is left.
func (l *Linker) resolve(text, key string) (string, error) {
	if len(Placeholders(text)) == 0 {
		return text, nil
	}
	t, err := l.renderTable()
	if err != nil {
		return "", err
	}
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		if v, ok := t[key][placeholder.FindStringSubmatch(m)[1]]; ok {
			return v
		}
		return m
	}), nil
}

// tierTexts is every store text rendered for a harness: the briefing, then
// its instruction files, by store path.
func (l *Linker) tierTexts(key string) ([]string, map[string]string) {
	var order []string
	texts := map[string]string{}
	if data, ok := l.briefingSource(); ok {
		order = append(order, BriefingFile)
		texts[BriefingFile] = string(data)
	}
	for _, f := range l.instructionFiles(key) {
		data, _ := os.ReadFile(f)
		rel, _ := filepath.Rel(l.StoreDir, f)
		rel = filepath.ToSlash(rel)
		order = append(order, rel)
		texts[rel] = string(data)
	}
	return order, texts
}

// missingTiers is the tiers the harness's texts use with no name in the
// table, and the files that use them.
func (l *Linker) missingTiers(key string, t renderTable) (missing, files []string) {
	order, texts := l.tierTexts(key)
	for _, rel := range order {
		used := false
		for _, k := range Placeholders(texts[rel]) {
			if _, ok := t[key][k]; ok {
				continue
			}
			used = true
			if !contains(missing, k) {
				missing = append(missing, k)
			}
		}
		if used {
			files = append(files, "agents/"+rel)
		}
	}
	return missing, files
}

// tierError refuses a harness's render when a tier it uses has no name for
// it, naming every missing tier, the files and the fix.
func (l *Linker) tierError(key string) error {
	t, err := l.renderTable()
	if err != nil {
		return err
	}
	missing, files := l.missingTiers(key, t)
	if len(missing) == 0 {
		return nil
	}
	var unknown, unset, lines []string
	for _, k := range missing {
		known := false
		for _, tab := range t {
			if _, ok := tab[k]; ok {
				known = true
			}
		}
		if known {
			unset = append(unset, k)
		} else {
			unknown = append(unknown, k)
		}
		lines = append(lines, fmt.Sprintf("%s = \"...\"", k))
	}
	var what []string
	if len(unknown) > 0 {
		what = append(what, fmt.Sprintf("%s, which agents/%s doesn't define", tiers(unknown), RenderFile))
	}
	if len(unset) > 0 {
		what = append(what, fmt.Sprintf("%s, which it names for other harnesses only", tiers(unset)))
	}
	verb := "uses"
	if len(files) > 1 {
		verb = "use"
	}
	return fmt.Errorf("%s %s %s; nothing was rendered for %s; add %s under [%s] in agents/%s",
		strings.Join(files, " and "), verb, strings.Join(what, ", and "), key, strings.Join(lines, ", "), key, RenderFile)
}

func tiers(ks []string) string {
	q := make([]string, len(ks))
	for i, k := range ks {
		q[i] = "{{model:" + k + "}}"
	}
	return strings.Join(q, ", ")
}

// Unresolved lists, per harness, the tiers the store's text uses that
// render.toml has no name for: the harnesses linked here and every one with
// a table. "codex: fast, top".
func (l *Linker) Unresolved() []string {
	t, err := l.renderTable()
	if err != nil {
		return []string{err.Error()}
	}
	keys := map[string]bool{}
	for k := range t {
		keys[k] = true
	}
	if st, err := l.State(); err == nil {
		for _, h := range st.Harnesses {
			if w, ok := wirings[h]; ok && w.instructions != "" {
				keys[w.key] = true
			}
		}
	}
	var out []string
	for _, key := range renderKeys {
		if !keys[key] {
			continue
		}
		if missing, _ := l.missingTiers(key, t); len(missing) > 0 {
			sort.Strings(missing)
			out = append(out, key+": "+strings.Join(missing, ", "))
		}
	}
	return out
}
