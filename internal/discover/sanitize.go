package discover

import (
	"strings"
	"unicode"

	"github.com/zacharytgray/yonder/internal/registry"
)

// caps on discovered text. a model server or tool can print anything, and
// agents read these strings back from `ls --json`.
const (
	maxText = 120
	maxPath = 512
)

// clean drops control and format characters (escapes, bidi overrides,
// zero-width) and cuts s to limit runes.
func clean(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
			continue
		}
		if n == limit {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// sanitize cleans every string discovery found before it enters a record.
func sanitize(f Facts) Facts {
	f.Hostname = clean(f.Hostname, maxText)
	f.OSVersion = clean(f.OSVersion, maxText)
	f.CPU = clean(f.CPU, maxText)
	gpus := make([]registry.GPU, len(f.GPUs))
	for i, g := range f.GPUs {
		g.Name, g.Vendor = clean(g.Name, maxText), clean(g.Vendor, maxText)
		gpus[i] = g
	}
	f.GPUs = gpus
	var models []registry.Model
	for _, m := range f.Models {
		m.Name, m.Runtime, m.Source = clean(m.Name, maxText), clean(m.Runtime, maxText), clean(m.Source, maxText)
		if m.Name != "" {
			models = append(models, m)
		}
	}
	f.Models = models
	hs := make([]registry.Harness, len(f.Harnesses))
	for i, h := range f.Harnesses {
		h.Name, h.Version, h.Path = clean(h.Name, maxText), clean(h.Version, maxText), clean(h.Path, maxPath)
		hs[i] = h
	}
	f.Harnesses = hs
	return f
}
