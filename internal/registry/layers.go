package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// layer names. connecting the machines is always on; these sit on top of it.
const (
	LayerSecrets = "secrets"
	LayerAgents  = "agents"
	LayerMemory  = "memory"
)

// LayerInfo describes one optional layer.
type LayerInfo struct {
	Name      string
	Summary   string
	Available bool // false: named so the slot exists, not in this version yet
}

// Layers is every layer this version knows, in display order.
var Layers = []LayerInfo{
	{Name: LayerSecrets, Summary: "run commands with secrets from 1Password or Bitwarden", Available: true},
	{Name: LayerAgents, Summary: "sync agent instructions, skills, subagents and hooks between machines", Available: true},
	{Name: LayerMemory, Summary: "shared agent memory (not in this version yet)", Available: false},
}

// LookupLayer finds a layer by name.
func LookupLayer(name string) (LayerInfo, bool) {
	for _, l := range Layers {
		if l.Name == name {
			return l, true
		}
	}
	return LayerInfo{}, false
}

// LayerNames lists the layers that can be turned on in this version.
func LayerNames() []string {
	var out []string
	for _, l := range Layers {
		if l.Available {
			out = append(out, l.Name)
		}
	}
	return out
}

// LayerState is one layer's fleet-wide setting. newest updated_at wins.
type LayerState struct {
	On        bool      `toml:"on" json:"on"`
	UpdatedAt time.Time `toml:"updated_at" json:"updated_at"`
	By        string    `toml:"by,omitempty" json:"by,omitempty"` // machine that set it
}

// LayerSettings maps layer name to its state. a layer missing here uses its default.
type LayerSettings map[string]LayerState

func (s *Store) layersPath() string { return filepath.Join(s.Dir, "layers.toml") }

// LayerSettings reads layers.toml. missing is empty.
func (s *Store) LayerSettings() (LayerSettings, error) {
	ls := LayerSettings{}
	err := readTOML(s.layersPath(), &ls)
	if errors.Is(err, os.ErrNotExist) {
		return LayerSettings{}, nil
	}
	return ls, err
}

func (s *Store) saveLayers(ls LayerSettings) error {
	if len(ls) == 0 {
		err := os.Remove(s.layersPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return writeTOML(s.layersPath(), ls)
}

// SetLayer turns a layer on or off here, dated now. sync carries it to the fleet.
func (s *Store) SetLayer(name string, on bool, by string) error {
	if _, ok := LookupLayer(name); !ok {
		return fmt.Errorf("no layer named %s (layers: %s)", name, strings.Join(LayerNames(), ", "))
	}
	ls, err := s.LayerSettings()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	if cur, ok := ls[name]; ok && !now.After(cur.UpdatedAt) {
		now = cur.UpdatedAt.Add(time.Second) // always newer than what it replaces
	}
	ls[name] = LayerState{On: on, UpdatedAt: now, By: by}
	return s.saveLayers(ls)
}

// LayerOn is whether a layer is on for this machine. a layer nobody has set
// uses its default: secrets is on where a provider is set up, which is how a
// fleet from before layers keeps working; the others start off.
func (s *Store) LayerOn(name string) bool {
	ls, _ := s.LayerSettings()
	if st, ok := ls[name]; ok {
		return st.On
	}
	if name == LayerSecrets {
		c, err := s.Config()
		if err != nil {
			return false
		}
		p, _ := c.Secrets["provider"].(string)
		return strings.TrimSpace(p) != ""
	}
	return false
}

// LayersOn maps every known layer to whether it is on here.
func (s *Store) LayersOn() map[string]bool {
	out := map[string]bool{}
	for _, l := range Layers {
		out[l.Name] = s.LayerOn(l.Name)
	}
	return out
}

// mergeLayers keeps the newest state per layer. a tie keeps the first seen.
// unknown names pass through, so an older yonder relays a newer one's layers.
func mergeLayers(sets ...LayerSettings) LayerSettings {
	out := LayerSettings{}
	for _, ls := range sets {
		for name, st := range ls {
			if ValidName(name) != nil {
				continue
			}
			if cur, ok := out[name]; !ok || st.UpdatedAt.After(cur.UpdatedAt) {
				out[name] = st
			}
		}
	}
	return out
}

func layersDigest(ls LayerSettings) string {
	names := make([]string, 0, len(ls))
	for n := range ls {
		names = append(names, n)
	}
	sort.Strings(names)
	d := ""
	for _, n := range names {
		d += fmt.Sprintf("l:%s=%t@%s;", n, ls[n].On, ls[n].UpdatedAt.UTC())
	}
	return d
}
