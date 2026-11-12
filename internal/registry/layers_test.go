package registry

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMergeLayersNewestWins(t *testing.T) {
	a := Bundle{Layers: LayerSettings{
		LayerAgents:  {On: true, UpdatedAt: t0, By: "laptop"},
		LayerSecrets: {On: true, UpdatedAt: t0.Add(time.Hour), By: "laptop"},
	}}
	b := Bundle{Layers: LayerSettings{
		LayerAgents:  {On: false, UpdatedAt: t0.Add(time.Minute), By: "gpu-box"},
		LayerSecrets: {On: false, UpdatedAt: t0, By: "gpu-box"},
		"future":     {On: true, UpdatedAt: t0}, // a newer yonder's layer passes through
		"Not Valid":  {On: true, UpdatedAt: t0},
	}}
	for _, got := range []Bundle{Merge(a, b), Merge(b, a)} {
		ls := got.Layers
		if ls[LayerAgents].On || ls[LayerAgents].By != "gpu-box" {
			t.Fatalf("newer agents off should win: %+v", ls)
		}
		if !ls[LayerSecrets].On {
			t.Fatalf("newer secrets on should win: %+v", ls)
		}
		if _, ok := ls["future"]; !ok {
			t.Fatalf("unknown layer should pass through: %+v", ls)
		}
		if _, ok := ls["Not Valid"]; ok {
			t.Fatalf("bad name kept: %+v", ls)
		}
	}
	if Merge(a).Digest() == Merge(a, b).Digest() {
		t.Fatal("digest should change with the layers")
	}
}

func TestFromPeerDropsFutureLayers(t *testing.T) {
	now := t0
	in := Bundle{Layers: LayerSettings{
		LayerAgents:  {On: true, UpdatedAt: now.Add(10 * time.Minute)},
		LayerSecrets: {On: true, UpdatedAt: now.Add(time.Minute)},
	}}
	got := FromPeer(Bundle{}, in, "laptop", now)
	if _, ok := got.Layers[LayerAgents]; ok {
		t.Fatalf("a layer dated past the skew guard should be dropped: %+v", got.Layers)
	}
	if !got.Layers[LayerSecrets].On {
		t.Fatalf("a layer within the guard should pass: %+v", got.Layers)
	}
}

func TestLayerDefaultsAndSet(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if s.LayerOn(LayerSecrets) || s.LayerOn(LayerAgents) {
		t.Fatal("a fresh machine starts with every layer off")
	}
	// a fleet from before layers with a provider keeps secrets on
	if err := s.SetSecrets(map[string]any{"provider": "1password", "mode": "desktop"}); err != nil {
		t.Fatal(err)
	}
	if !s.LayerOn(LayerSecrets) {
		t.Fatal("a provider set up means secrets is on until someone turns it off")
	}
	if err := s.SetLayer(LayerSecrets, false, "laptop"); err != nil {
		t.Fatal(err)
	}
	if s.LayerOn(LayerSecrets) {
		t.Fatal("an explicit off beats the default")
	}
	first, _ := s.LayerSettings()
	if err := s.SetLayer(LayerSecrets, true, "laptop"); err != nil {
		t.Fatal(err)
	}
	second, _ := s.LayerSettings()
	if !second[LayerSecrets].UpdatedAt.After(first[LayerSecrets].UpdatedAt) {
		t.Fatal("a second change in the same second must still be newer")
	}
	if err := s.SetLayer("nope", true, "laptop"); err == nil {
		t.Fatal("unknown layer should be refused")
	}
	// layers ride in the bundle and survive a merge from a peer that knows none
	b, err := s.Bundle()
	if err != nil || !b.Layers[LayerSecrets].On {
		t.Fatalf("bundle: %+v %v", b.Layers, err)
	}
	if _, err := s.MergeFrom(Bundle{}, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	if !s.LayerOn(LayerSecrets) {
		t.Fatal("an older peer's reply without layers must not clear them")
	}
	data, _ := json.Marshal(Bundle{Machines: []Machine{}, Tombstones: []Tombstone{}})
	if string(data) != `{"machines":[],"tombstones":[]}` {
		t.Fatalf("no layers key when empty, for older peers: %s", data)
	}
	if err := s.Forget(); err != nil {
		t.Fatal(err)
	}
	if ls, _ := s.LayerSettings(); len(ls) != 0 {
		t.Fatalf("forget should drop the fleet's layers: %+v", ls)
	}
}

// a fleet that wrote the old memory slot: it counts as no layer here, and
// still passes through sync like any name this version doesn't know
func TestUnknownLayerInFile(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := writeFile(s.layersPath(), []byte("[memory]\non = true\nupdated_at = 2026-11-11T20:00:00Z\n")); err != nil {
		t.Fatal(err)
	}
	if on := s.LayersOn(); len(on) != 2 || on["memory"] {
		t.Fatalf("LayersOn: %v", on)
	}
	if err := s.SetLayer("memory", true, "laptop"); err == nil {
		t.Fatal("memory is not a layer")
	}
	b, err := s.Bundle()
	if err != nil || !b.Layers["memory"].On {
		t.Fatalf("an unknown layer should pass through: %+v %v", b.Layers, err)
	}
	if _, err := s.MergeFrom(Bundle{}, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	if ls, _ := s.LayerSettings(); !ls["memory"].On {
		t.Fatalf("a merge should keep it: %+v", ls)
	}
}
