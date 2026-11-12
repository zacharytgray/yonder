package tailnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"
)

const macAppBinary = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

var (
	ErrNotInstalled = errors.New("tailscale not found; install it from https://tailscale.com/download")
	ErrNotRunning   = errors.New("tailscale is not running or not logged in; run `tailscale up`")
)

type Peer struct {
	HostName     string    `json:"host_name"`
	DNSName      string    `json:"dns_name"`
	TailscaleIPs []string  `json:"tailscale_ips"`
	OS           string    `json:"os"`
	Online       bool      `json:"online"`
	LastSeen     time.Time `json:"last_seen"`
	UserID       int64     `json:"user_id"`
	ShareeNode   bool      `json:"sharee_node"` // shared into this tailnet by another account
}

type Status struct {
	BackendState string
	Self         Peer
	Peers        []Peer
}

// Name is the yonder name for a peer: first label of DNSName, kebab-cased.
func (p Peer) Name() string { return ShortName(p.DNSName, p.HostName) }

// IP returns the first IPv4 tailscale address, or the first address.
func (p Peer) IP() string {
	for _, ip := range p.TailscaleIPs {
		if !strings.Contains(ip, ":") {
			return ip
		}
	}
	if len(p.TailscaleIPs) > 0 {
		return p.TailscaleIPs[0]
	}
	return ""
}

// GOOS maps tailscale's OS names onto GOOS values.
func (p Peer) GOOS() string {
	switch strings.ToLower(p.OS) {
	case "macos", "darwin":
		return "darwin"
	default:
		return strings.ToLower(p.OS)
	}
}

// Mobile reports phones and tablets, which yonder can't run on.
func (p Peer) Mobile() bool {
	switch strings.ToLower(p.OS) {
	case "ios", "android", "tvos", "ipados":
		return true
	}
	return false
}

// Lookup finds a peer (or self) by yonder name. with several of that name it
// returns the first; Match and ForRecord tell them apart.
func (s *Status) Lookup(name string) (Peer, bool) {
	if ps := s.Match(name); len(ps) > 0 {
		return ps[0], true
	}
	return Peer{}, false
}

// Match returns every peer (self first) that name could mean: a full dns name
// matches exactly, anything else matches the yonder name.
func (s *Status) Match(name string) []Peer {
	full := strings.TrimSuffix(strings.ToLower(name), ".")
	var out []Peer
	for _, p := range append([]Peer{s.Self}, s.Peers...) {
		if strings.Contains(full, ".") {
			if strings.EqualFold(p.DNSName, full) {
				out = append(out, p)
			}
		} else if p.Name() == name {
			out = append(out, p)
		}
	}
	return out
}

// ForRecord finds the peer behind a registry record: among same-named peers,
// the one with the record's dns name, else the one at its address, else the
// only one. callers still check the address (Reach).
func (s *Status) ForRecord(name, dnsName, ip string) (Peer, bool) {
	ps := s.Match(name)
	for _, p := range ps {
		if dnsName != "" && strings.EqualFold(p.DNSName, dnsName) {
			return p, true
		}
	}
	for _, p := range ps {
		if ip != "" && slices.Contains(p.TailscaleIPs, ip) {
			return p, true
		}
	}
	if len(ps) == 1 {
		return ps[0], true
	}
	return Peer{}, false
}

// Reachability is what Reach found for a recorded peer.
type Reachability int

const (
	Reachable      Reachability = iota
	NotInTailnet                // no peer by that name (or several, none of them the record's)
	Offline                     // tailscale says offline
	AddressChanged              // its tailnet address isn't the pinned one
)

// Reach is the one check before any connection to a recorded peer: it is in
// the tailnet, online, and still at the address the record pinned.
func (s *Status) Reach(name, dnsName, ip string) (Peer, Reachability) {
	p, ok := s.ForRecord(name, dnsName, ip)
	switch {
	case !ok:
		return p, NotInTailnet
	case !p.Online:
		return p, Offline
	case ip != p.IP():
		return p, AddressChanged
	}
	return p, Reachable
}

// Owned reports whether p belongs to the account this machine is logged in as.
// a node shared in from another account (node sharing) is not owned.
// with no user id for self (old fixtures) every peer counts as owned.
func (s *Status) Owned(p Peer) bool {
	if p.ShareeNode {
		return false
	}
	return s.Self.UserID == 0 || p.UserID == s.Self.UserID
}

// LookupIP finds a peer (or self) by one of its tailscale addresses.
func (s *Status) LookupIP(ip string) (Peer, bool) {
	for _, p := range append([]Peer{s.Self}, s.Peers...) {
		if slices.Contains(p.TailscaleIPs, ip) {
			return p, true
		}
	}
	return Peer{}, false
}

// ShortName takes the first DNS label (falling back to the hostname) and kebab-cases it.
func ShortName(dnsName, hostName string) string {
	s := strings.TrimSuffix(dnsName, ".")
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		s = hostName
	}
	return Kebab(s)
}

func Kebab(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

type rawPeer struct {
	HostName     string
	DNSName      string
	TailscaleIPs []string
	OS           string
	Online       bool
	LastSeen     time.Time
	UserID       int64
	ShareeNode   bool
}

type rawStatus struct {
	BackendState string
	Self         *rawPeer
	Peer         map[string]*rawPeer
}

func (r *rawPeer) peer() Peer {
	return Peer{
		HostName:     r.HostName,
		DNSName:      strings.TrimSuffix(r.DNSName, "."),
		TailscaleIPs: r.TailscaleIPs,
		OS:           r.OS,
		Online:       r.Online,
		LastSeen:     r.LastSeen.UTC(),
		UserID:       r.UserID,
		ShareeNode:   r.ShareeNode,
	}
}

// Parse reads `tailscale status --json` output.
func Parse(data []byte) (*Status, error) {
	var raw rawStatus
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse tailscale status: %w", err)
	}
	st := &Status{BackendState: raw.BackendState}
	if raw.Self != nil {
		st.Self = raw.Self.peer()
		st.Self.Online = true
	}
	for _, p := range raw.Peer {
		if p != nil {
			st.Peers = append(st.Peers, p.peer())
		}
	}
	sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].Name() < st.Peers[j].Name() })
	return st, nil
}

// Binary finds the tailscale CLI.
func Binary() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	if _, err := os.Stat(macAppBinary); err == nil {
		return macAppBinary, nil
	}
	return "", ErrNotInstalled
}

// Get returns the live status, or the YONDER_TAILSCALE_JSON fixture when set.
func Get(ctx context.Context) (*Status, error) {
	var data []byte
	if f := os.Getenv("YONDER_TAILSCALE_JSON"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("YONDER_TAILSCALE_JSON: %w", err)
		}
		data = b
	} else {
		bin, err := Binary()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, bin, "status", "--json")
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil && len(out) == 0 {
			return nil, fmt.Errorf("%w (%s)", ErrNotRunning, strings.TrimSpace(stderr.String()))
		}
		data = out
	}
	st, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if st.BackendState != "" && st.BackendState != "Running" {
		return nil, fmt.Errorf("%w (state %s)", ErrNotRunning, st.BackendState)
	}
	return st, nil
}
