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

func loadShared(t *testing.T) *Status {
	t.Helper()
	data, err := os.ReadFile("testdata/shared.json")
	if err != nil {
		t.Fatal(err)
	}
	st, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// a node shared in from another account can carry the same short name as yours
func TestMatchAndOwnership(t *testing.T) {
	st := loadShared(t)
	ps := st.Match("gpu-box")
	if len(ps) != 2 {
		t.Fatalf("want both gpu-boxes, got %+v", ps)
	}
	mine := st.Match("gpu-box.example.ts.net.")
	if len(mine) != 1 || mine[0].IP() != "100.64.0.2" || !st.Owned(mine[0]) {
		t.Fatalf("full name: %+v", mine)
	}
	theirs := st.Match("GPU-BOX.other.example.ts.net")
	if len(theirs) != 1 || st.Owned(theirs[0]) {
		t.Fatalf("foreign gpu-box should match by full name and not be owned: %+v", theirs)
	}
	if nas := st.Match("nas"); len(nas) != 1 || st.Owned(nas[0]) {
		t.Fatalf("nas is another user's: %+v", nas)
	}
	if fb := st.Match("friend-box"); len(fb) != 1 || st.Owned(fb[0]) {
		t.Fatalf("a sharee node is not owned: %+v", fb)
	}
	if d := st.Match("desktop-pc"); len(d) != 1 || !st.Owned(d[0]) {
		t.Fatalf("desktop-pc: %+v", d)
	}
	// a record finds its own peer among same-named ones, by dns name or address
	if p, ok := st.ForRecord("gpu-box", "gpu-box.example.ts.net", "100.64.0.2"); !ok || p.IP() != "100.64.0.2" {
		t.Fatalf("for record by dns: %+v", p)
	}
	if p, ok := st.ForRecord("gpu-box", "", "100.64.0.2"); !ok || p.IP() != "100.64.0.2" {
		t.Fatalf("for record by ip: %+v", p)
	}
	if _, ok := st.ForRecord("gpu-box", "", "100.64.0.99"); ok {
		t.Fatal("two gpu-boxes and neither is the record's: no match")
	}
}

func TestReach(t *testing.T) {
	st := loadShared(t)
	if _, r := st.Reach("desktop-pc", "desktop-pc.example.ts.net", "100.64.0.3"); r != Reachable {
		t.Fatalf("desktop-pc: %v", r)
	}
	if _, r := st.Reach("desktop-pc", "desktop-pc.example.ts.net", "100.64.0.30"); r != AddressChanged {
		t.Fatalf("moved: %v", r)
	}
	// the record's gpu-box is offline; the online foreign one must not stand in for it
	if _, r := st.Reach("gpu-box", "gpu-box.example.ts.net", "100.64.0.2"); r != Offline {
		t.Fatalf("gpu-box: %v", r)
	}
	if _, r := st.Reach("nope", "", ""); r != NotInTailnet {
		t.Fatalf("nope: %v", r)
	}
}

// F2: ownership fails closed when either side has no user id
func TestOwnedFailsClosedWithoutUserID(t *testing.T) {
	data := []byte(`{"BackendState":"Running",
 "Self":{"HostName":"laptop","DNSName":"laptop.example.ts.net.","TailscaleIPs":["100.64.0.1"]},
 "Peer":{"a":{"HostName":"gpu-box","DNSName":"gpu-box.example.ts.net.","TailscaleIPs":["100.64.0.2"],"UserID":1001}}}`)
	st, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	p := st.Peers[0]
	if st.Owned(p) {
		t.Fatal("no user id for self must not own every peer")
	}
	if why := st.NotOwnedWhy(p); why != "tailscale status gives no user id to check its owner" {
		t.Fatalf("why: %q", why)
	}
	st.Self.UserID = 1001
	if !st.Owned(p) {
		t.Fatal("same user should own it")
	}
	p.UserID = 0
	if st.Owned(p) {
		t.Fatal("a peer with no user id is not owned")
	}
}

// a tagged node has the tag owner's user id; it reads as not yours, and says so
func TestTaggedNodeNotOwned(t *testing.T) {
	data := []byte(`{"BackendState":"Running",
 "Self":{"HostName":"laptop","DNSName":"laptop.example.ts.net.","TailscaleIPs":["100.64.0.1"],"UserID":1001},
 "Peer":{"a":{"HostName":"build-box","DNSName":"build-box.example.ts.net.","TailscaleIPs":["100.64.0.4"],"UserID":3003,"Tags":["tag:server","tag:ci"]}}}`)
	st, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	p := st.Peers[0]
	if st.Owned(p) || len(p.Tags) != 2 {
		t.Fatalf("tagged: %+v", p)
	}
	if why := st.NotOwnedWhy(p); why != "it is a tagged device (tag:server, tag:ci), and tagged devices count as not yours" {
		t.Fatalf("why: %q", why)
	}
}
