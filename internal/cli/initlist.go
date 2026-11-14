package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

// listView is `yonder init --list --json`: what init would offer, read-only.
type listView struct {
	Self           *listSelf       `json:"self"`
	TailscaleError string          `json:"tailscale_error"`
	Next           string          `json:"next,omitempty"` // what to do about tailscale_error
	Layers         map[string]bool `json:"layers"`
	Peers          []listPeer      `json:"peers"`
}

type listSelf struct {
	Name      string `json:"name"`
	OS        string `json:"os"`
	IP        string `json:"tailscale_ip"`
	DNSName   string `json:"dns_name"`
	SSHD      bool   `json:"sshd"`       // something answers on port 22 here
	SetUp     bool   `json:"set_up"`     // init ran here already
	ValidName bool   `json:"valid_name"` // yonder can use its tailscale name
}

type listPeer struct {
	Name     string   `json:"name"`
	DNSName  string   `json:"dns_name"`
	OS       string   `json:"os"`
	IP       string   `json:"tailscale_ip"`
	Online   bool     `json:"online"`
	LastSeen string   `json:"last_seen,omitempty"`
	Owned    bool     `json:"owned"`     // the logged-in tailscale account owns it
	InFleet  bool     `json:"in_fleet"`  // already set up here
	Pending  bool     `json:"pending"`   // in the fleet, setup unfinished
	CanAdd   bool     `json:"can_add"`   // init --machines would take it as is
	Note     string   `json:"note"`      // why not, or what to know
	SSH      *sshView `json:"ssh"`       // null when not probed (offline, or --no-probe)
	AddFlags []string `json:"add_flags"` // flags it needs: --allow-shared, its full name
}

type sshView struct {
	PortOpen bool   `json:"port_open"`
	KeyAuth  *bool  `json:"key_auth"` // null when the port is closed
	Error    string `json:"error,omitempty"`
	Fix      string `json:"fix,omitempty"`
}

// initList prints what init would offer and changes nothing.
func (a *app) initList(asJSON, probe bool, user string, allowShared bool) error {
	v := listView{Peers: []listPeer{}, Layers: a.store.LayersOn()}
	st, err := a.status()
	if err != nil {
		v.TailscaleError = err.Error()
		switch {
		case errors.Is(err, tailnet.ErrNotInstalled):
			v.Next = "install Tailscale on this machine (https://tailscale.com/download), log in, then run this again"
		default:
			v.Next = "start Tailscale and log in (`tailscale up`, or open the Tailscale app), then run this again"
		}
		if asJSON {
			_ = writeJSON(a.out, v)
			return quietExit(ExitError)
		}
		return fmt.Errorf("%w; %s", err, v.Next)
	}
	cfg, _ := a.store.Config()
	self := st.Self.Name()
	v.Self = &listSelf{Name: self, OS: st.Self.GOOS(), IP: st.Self.IP(), DNSName: st.Self.DNSName,
		SSHD: portOpen("127.0.0.1", 22, time.Second), SetUp: cfg.Self != "", ValidName: registry.ValidName(self) == nil}
	if user == "" {
		user = localUser()
	}
	cands := a.initCandidates(st, self, allowShared)
	for _, c := range cands {
		p := c.peer
		lp := listPeer{Name: p.Name(), DNSName: p.DNSName, OS: p.GOOS(), IP: p.IP(), Online: p.Online, Owned: !c.shared, Note: c.note, AddFlags: []string{}}
		if !p.Online {
			lp.LastSeen = ago(p.LastSeen)
		}
		if m, err := a.store.Machine(p.Name()); err == nil {
			lp.InFleet, lp.Pending = !m.Pending, m.Pending
		}
		lp.CanAdd = c.note == "" || strings.HasPrefix(c.note, "removed earlier")
		if lab := label(c, cands); lab != p.Name() {
			lp.AddFlags = append(lp.AddFlags, "--machines "+lab)
			if lp.Note == "" {
				lp.Note = "another peer has the same short name; use its full name"
			}
		}
		if c.shared {
			lp.AddFlags = append(lp.AddFlags, "--allow-shared")
			if allowShared && lp.Note == "" {
				lp.Note = "not owned by your tailscale account (" + st.NotOwnedWhy(p) + "); add it only if it really is yours"
			}
		}
		if lp.CanAdd && !p.Online {
			lp.Note = "offline: init saves it as pending, and `yonder add " + p.Name() + "` finishes it once it is online"
		}
		v.Peers = append(v.Peers, lp)
	}
	if probe {
		var wg sync.WaitGroup
		for i := range v.Peers {
			lp := &v.Peers[i]
			if !lp.Online || (!lp.CanAdd && !lp.InFleet && !lp.Pending) {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				lp.SSH = a.probeSSH(lp.Name, lp.IP, user)
			}()
		}
		wg.Wait()
	}
	if asJSON {
		return writeJSON(a.out, v)
	}
	s := v.Self
	sshd := "ssh is on"
	if !s.SSHD {
		sshd = "nothing answers on port 22: turn on SSH (macOS: System Settings > General > Sharing > Remote Login) so other machines can reach this one"
	}
	fmt.Fprintf(a.out, "this machine: %s (%s, %s); %s\n", s.Name, s.OS, s.IP, sshd)
	if len(v.Peers) == 0 {
		fmt.Fprintln(a.out, "no other machines in this tailnet")
	}
	for _, p := range v.Peers {
		state := "online"
		if !p.Online {
			state = "offline " + p.LastSeen
		}
		line := fmt.Sprintf("  %-20s %-7s %-14s", p.Name, p.OS, state)
		switch {
		case p.InFleet:
			line += " in the fleet"
		case p.CanAdd:
			line += " can join"
		default:
			line += " can't join"
		}
		if p.SSH != nil {
			switch {
			case !p.SSH.PortOpen:
				line += "; ssh: port closed"
			case p.SSH.KeyAuth != nil && *p.SSH.KeyAuth:
				line += "; ssh: ok"
			default:
				line += "; ssh: key not trusted yet"
			}
		}
		if p.Note != "" {
			line += " (" + p.Note + ")"
		}
		fmt.Fprintln(a.out, line)
	}
	return nil
}

func portOpen(host string, port int, timeout time.Duration) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), timeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// probeSSH checks port 22 and whether this machine's key gets in. it pins
// nothing: the host key goes to a temp file that is deleted.
func (a *app) probeSSH(name, ip, user string) *sshView {
	v := &sshView{PortOpen: portOpen(ip, 22, 3*time.Second)}
	if !v.PortOpen {
		v.Error = "nothing answers on port 22"
		v.Fix = "turn on SSH on " + name + " (macOS: System Settings > General > Sharing > Remote Login; Linux: install and start openssh-server)"
		return v
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	t := transport.Target{Name: name, Host: ip, Port: 22, User: user, Identity: registry.ExpandHome(a.store.Identity(name))}
	var errb bytes.Buffer
	_, code, err := a.ssh.Pin(ctx, t, "exit 0", transport.IO{Stderr: &errb})
	ok := err == nil && code == 0
	v.KeyAuth = &ok
	if !ok {
		if ue, isU := transport.IsUnreachable(err); isU {
			v.Error = ue.Detail
			if ue.AuthFailed() {
				v.Fix = fmt.Sprintf("copy this machine's key there: `ssh-copy-id %s@%s` (the user types the password for %s once), or name the key you use with --identity %s=<path>", user, ip, name, name)
			}
		} else if err != nil {
			v.Error = err.Error()
		}
	}
	return v
}
