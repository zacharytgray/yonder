package tailnet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func load(t *testing.T) *Status {
	t.Helper()
	data, err := os.ReadFile("testdata/status.json")
	if err != nil {
		t.Fatal(err)
	}
	st, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestParse(t *testing.T) {
	st := load(t)
	if st.Self.Name() != "laptop" || st.Self.IP() != "100.64.0.1" || !st.Self.Online || st.Self.GOOS() != "darwin" {
		t.Fatalf("self: %+v", st.Self)
	}
	if st.Self.DNSName != "laptop.example.ts.net" {
		t.Fatalf("trailing dot should be trimmed: %q", st.Self.DNSName)
	}
	if len(st.Peers) != 4 {
		t.Fatalf("want 4 peers, got %d", len(st.Peers))
	}

	gpu, ok := st.Lookup("gpu-box")
	if !ok || gpu.Online {
		t.Fatalf("gpu-box should be offline: %+v", gpu)
	}
	if !gpu.LastSeen.Equal(time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("last seen: %v", gpu.LastSeen)
	}

	desk, ok := st.Lookup("desktop-pc")
	if !ok || !desk.Online || !desk.LastSeen.IsZero() {
		t.Fatalf("desktop-pc: %+v", desk)
	}

	old, _ := st.Lookup("old-server")
	if old.Online || !old.LastSeen.IsZero() {
		t.Fatalf("never-seen peer should have zero last seen: %+v", old)
	}

	phone, _ := st.Lookup("my-phone")
	if !phone.Mobile() || gpu.Mobile() {
		t.Fatal("mobile detection")
	}
	if _, ok := st.Lookup("nope"); ok {
		t.Fatal("lookup of unknown peer")
	}
}

func TestShortName(t *testing.T) {
	cases := []struct{ dns, host, want string }{
		{"gpu-box.example.ts.net.", "whatever", "gpu-box"},
		{"desktop-pc.example.ts.net", "", "desktop-pc"},
		{"", "My_Laptop", "my-laptop"},
		{"", "Work Station (2)", "work-station-2"},
		{"--weird--.example.ts.net", "", "weird"},
	}
	for _, c := range cases {
		if got := ShortName(c.dns, c.host); got != c.want {
			t.Errorf("ShortName(%q, %q) = %q, want %q", c.dns, c.host, got, c.want)
		}
	}
}

func TestIPPrefersV4(t *testing.T) {
	p := Peer{TailscaleIPs: []string{"fd7a:115c:a1e0::9", "100.64.0.9"}}
	if p.IP() != "100.64.0.9" {
		t.Fatal(p.IP())
	}
	if (Peer{TailscaleIPs: []string{"fd7a::1"}}).IP() != "fd7a::1" {
		t.Fatal("v6 fallback")
	}
}

func TestGetFixtureAndState(t *testing.T) {
	t.Setenv("YONDER_TAILSCALE_JSON", "testdata/status.json")
	st, err := Get(context.Background())
	if err != nil || st.Self.Name() != "laptop" {
		t.Fatalf("fixture: %v %+v", err, st)
	}

	f := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(f, []byte(`{"BackendState":"NeedsLogin","Peer":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YONDER_TAILSCALE_JSON", f)
	if _, err := Get(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
}
