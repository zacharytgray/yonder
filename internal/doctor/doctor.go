// Package doctor checks every piece of a yonder setup and says how to fix what's broken.
package doctor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/mesh"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/secrets"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

type Status string

const (
	Pass Status = "pass"
	Warn Status = "warn"
	Fail Status = "fail"
)

// StaleAfter is how old discovered_at can be before a record counts as stale.
const StaleAfter = 7 * 24 * time.Hour

// Check is one line of doctor output.
type Check struct {
	Group  string `json:"group"` // "this machine", "fleet", or a machine name
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

type Summary struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

// Report is the whole run, and the --json shape.
type Report struct {
	Self    string  `json:"self"`
	Version string  `json:"version"`
	OK      bool    `json:"ok"` // no check failed
	Summary Summary `json:"summary"`
	Checks  []Check `json:"checks"`
}

func NewReport(self, version string, checks []Check) Report {
	r := Report{Self: self, Version: version, Checks: checks}
	if r.Checks == nil {
		r.Checks = []Check{}
	}
	for _, c := range checks {
		switch c.Status {
		case Pass:
			r.Summary.Pass++
		case Warn:
			r.Summary.Warn++
		case Fail:
			r.Summary.Fail++
		}
	}
	r.OK = r.Summary.Fail == 0
	return r
}

// ExitCode is 0 with no FAIL, else 1.
func (r Report) ExitCode() int {
	if r.OK {
		return 0
	}
	return 1
}

// Remote is what doctor needs from another machine. errors that are a
// *transport.UnreachableError mean ssh failed; a *YonderError means ssh
// worked but the remote yonder didn't.
type Remote interface {
	Version(m registry.Machine) (string, error)
	Registry(m registry.Machine) (registry.Bundle, error)
	Skills(m registry.Machine) (agents.Report, error)
	AuthorizedKeys(m registry.Machine) (string, error)
}

// YonderError is a remote yonder that ran but exited non-zero.
type YonderError struct {
	Code   int
	Stderr string
}

func (e *YonderError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("exit %d", e.Code)
	}
	return fmt.Sprintf("exit %d: %s", e.Code, e.Stderr)
}

// Env is everything the checks look at. the cli fills it in; tests fake it.
type Env struct {
	Self         string // config self; "" when not set up
	Store        *registry.Store
	Status       *tailnet.Status
	StatusErr    error
	Fixture      string // YONDER_TAILSCALE_JSON, if set
	TailscaleBin func() (string, error)
	Version      string
	Home         string // ~ for skill folders and authorized_keys
	StateDir     string
	Secrets      []secrets.Check
	Layers       map[string]bool // fleet-wide layers, on or off here
	Remote       Remote
	Now          time.Time
}

const groupLocal = "this machine"

// Run runs every check.
func Run(e Env) []Check {
	if e.Now.IsZero() {
		e.Now = time.Now()
	}
	var out []Check
	add := func(group, name string, st Status, detail, fix string) {
		out = append(out, Check{Group: group, Name: name, Status: st, Detail: detail, Fix: fix})
	}

	tsOK := tailscaleChecks(e, func(n string, s Status, d, f string) { add(groupLocal, n, s, d, f) })

	ms, err := e.Store.Machines()
	if err != nil {
		add(groupLocal, "registry", Fail, err.Error(), "fix or delete the broken file named above")
	}
	tombs, _ := e.Store.Tombstones()
	haveSelf := selfChecks(e, ms, func(n string, s Status, d, f string) { add(groupLocal, n, s, d, f) })

	if err := writable(e.StateDir); err != nil {
		add(groupLocal, "log dir", Fail, fmt.Sprintf("%s is not writable: %v", e.StateDir, err),
			fmt.Sprintf("mkdir -p %s && chmod u+rwx %s", e.StateDir, e.StateDir))
	} else {
		add(groupLocal, "log dir", Pass, e.StateDir+" is writable", "")
	}

	out = append(out, skillChecks(groupLocal, agents.Status(e.Home), e.Home, "yonder agents install")...)

	lost := lostKeys(tombs, ms)
	var src []mesh.Source
	var skips []mesh.Skip
	if e.Status != nil {
		src, skips = mesh.Sources(e.Self, ms, tombs, e.Status, false)
	} else {
		for _, m := range ms { // nothing known: prune nothing
			skips = append(skips, mesh.Skip{Name: m.Name, Keep: true})
		}
	}
	fl := fleet{lost: lost, src: src, skips: skips, tombs: tombs, live: ms}
	data, keysErr := os.ReadFile(transport.AuthorizedKeysPath(e.Home))
	if keysErr != nil && !errors.Is(keysErr, os.ErrNotExist) {
		add(groupLocal, "authorized_keys", Warn, "can't read ~/.ssh/authorized_keys: "+keysErr.Error(), "")
	} else if haveSelf {
		out = append(out, meshChecks(groupLocal, e.Self, string(data), fl)...)
	}
	if len(lost) > 0 && (keysErr == nil || errors.Is(keysErr, os.ErrNotExist)) {
		cs := keyChecks(groupLocal, string(data), lost)
		// remove --lost won't write through a symlink; the lines go by hand
		if real, ok := symlinkedKeys(e.Home); ok {
			for i := range cs {
				if cs[i].Status == Fail {
					cs[i].Detail += " (authorized_keys is a symlink to " + real + ")"
					cs[i].Fix = "delete those lines from " + real + " by hand; `" + cs[i].Fix + "` prints them"
				}
			}
		}
		out = append(out, cs...)
	}

	out = append(out, identityChecks(e)...)

	if e.Layers != nil {
		add(groupLocal, "layers", Pass, layersDetail(e.Layers), "")
	}

	for _, s := range e.Secrets {
		st := Pass
		if !s.OK {
			st = Fail
		}
		add("secrets", s.Name, st, s.Detail, s.Fix)
	}

	if !haveSelf {
		add("fleet", "fleet", Warn, "skipped until this machine is set up", "")
		return out
	}
	if !tsOK {
		add("fleet", "fleet", Warn, "skipped: tailscale status is unavailable", "fix the tailscale lines above first")
		return out
	}
	out = append(out, fleetChecks(e, ms, fl)...)
	return out
}

func layersDetail(on map[string]bool) string {
	var yes, no []string
	for _, l := range registry.Layers {
		switch {
		case !l.Available:
		case on[l.Name]:
			yes = append(yes, l.Name)
		default:
			no = append(no, l.Name)
		}
	}
	d := "on: " + orDefault(strings.Join(yes, ", "), "none")
	if len(no) > 0 {
		d += "; off: " + strings.Join(no, ", ")
	}
	return d
}

// fleet is what the per-machine authorized_keys checks compare against.
type fleet struct {
	lost  []lostMachine
	src   []mesh.Source
	skips []mesh.Skip
	tombs []registry.Tombstone
	live  []registry.Machine
}

// meshChecks: every fleet machine's mesh key is in target's authorized_keys,
// no line yonder added for a removed machine is left, and no line yonder
// tagged is outside the grant set.
func meshChecks(group, target, content string, fl fleet) []Check {
	var out []Check
	if un := mesh.Unreached(content, target, fl.src); len(un) > 0 {
		out = append(out, Check{Group: group, Name: "mesh", Status: Warn,
			Detail: fmt.Sprintf("%s can't ssh in: none of %s keys on record are in ~/.ssh/authorized_keys", strings.Join(un, ", "), plural(len(un), "its", "their")),
			Fix:    "yonder mesh " + target})
	}
	left := mesh.Leftover(content, fl.tombs, fl.live)
	names := make([]string, 0, len(left))
	for n := range left {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		out = append(out, Check{Group: group, Name: "mesh", Status: Warn,
			Detail: fmt.Sprintf("%d line(s) yonder added for removed machine %s are still in ~/.ssh/authorized_keys", left[n], n),
			Fix:    "yonder remove " + n + " --yes  (removes them on every online machine)"})
	}
	_, _, pruned := mesh.Plan(content, target, "", fl.src, fl.skips)
	extra := map[string]int{}
	var who []string
	for _, l := range pruned {
		n := mesh.TagOf(l)
		if _, removed := left[n]; removed {
			continue // reported above
		}
		if extra[n] == 0 {
			who = append(who, n)
		}
		extra[n]++
	}
	if len(who) > 0 {
		slices.Sort(who)
		parts := make([]string, len(who))
		for i, n := range who {
			parts[i] = fmt.Sprintf("%s (%d)", n, extra[n])
		}
		out = append(out, Check{Group: group, Name: "mesh", Status: Warn,
			Detail: "trusts keys yonder added that mesh no longer grants, of " + strings.Join(parts, ", "),
			Fix:    "yonder mesh"})
	}
	if len(out) == 0 && len(fl.src) > 1 {
		out = append(out, Check{Group: group, Name: "mesh", Status: Pass, Detail: "every fleet machine's key is authorized here"})
	}
	return out
}

// identityChecks makes sure every ssh key set with `yonder ssh-key` still exists here.
func identityChecks(e Env) []Check {
	cfg, err := e.Store.Config()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(cfg.SSH))
	for n, c := range cfg.SSH {
		if c.Identity != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var out []Check
	for _, n := range names {
		id := cfg.SSH[n].Identity
		name := "ssh key for " + n
		st, err := os.Stat(registry.ExpandHome(id))
		switch {
		case err != nil:
			out = append(out, Check{Group: groupLocal, Name: name, Status: Fail, Detail: id + " does not exist",
				Fix: fmt.Sprintf("`yonder ssh-key %s <path>` points at the right key, or `yonder ssh-key %s --clear` drops it", n, n)})
		case st.IsDir():
			out = append(out, Check{Group: groupLocal, Name: name, Status: Fail, Detail: id + " is a directory, not a private key",
				Fix: fmt.Sprintf("`yonder ssh-key %s <path>`", n)})
		default:
			out = append(out, Check{Group: groupLocal, Name: name, Status: Pass, Detail: id})
		}
	}
	return out
}

func tailscaleChecks(e Env, add func(string, Status, string, string)) bool {
	if e.Fixture != "" {
		add("tailscale", Pass, "using the YONDER_TAILSCALE_JSON fixture "+e.Fixture, "")
	} else if e.TailscaleBin != nil {
		bin, err := e.TailscaleBin()
		if err != nil {
			add("tailscale installed", Fail, "the tailscale CLI was not found", "install Tailscale: https://tailscale.com/download")
			return false
		}
		add("tailscale installed", Pass, bin, "")
	}
	if e.StatusErr != nil {
		msg := e.StatusErr.Error()
		switch {
		case errors.Is(e.StatusErr, tailnet.ErrNotInstalled):
			add("tailscale installed", Fail, "the tailscale CLI was not found", "install Tailscale: https://tailscale.com/download")
		case strings.Contains(msg, "NeedsLogin") || strings.Contains(msg, "NeedsMachineAuth"):
			add("tailscale running", Pass, "tailscaled is up", "")
			add("tailscale logged in", Fail, "not logged in ("+backendState(msg)+")", "tailscale up")
		case strings.Contains(msg, "YONDER_TAILSCALE_JSON"):
			add("tailscale running", Fail, msg, "point YONDER_TAILSCALE_JSON at a readable status file, or unset it")
		default:
			add("tailscale running", Fail, msg, "start Tailscale (open the app, or `sudo systemctl start tailscaled`), then `tailscale up`")
		}
		return false
	}
	add("tailscale running", Pass, "backend "+orDefault(e.Status.BackendState, "Running"), "")
	add("tailscale logged in", Pass, "this machine is "+orDefault(e.Status.Self.DNSName, e.Status.Self.Name()), "")
	return true
}

func backendState(msg string) string {
	if _, after, ok := strings.Cut(msg, "(state "); ok {
		return strings.TrimSuffix(after, ")")
	}
	return "unknown state"
}

func selfChecks(e Env, ms []registry.Machine, add func(string, Status, string, string)) bool {
	if e.Self == "" {
		add("set up", Fail, "this machine has no fleet name yet", "yonder init")
		return false
	}
	for _, m := range ms {
		if m.Name != e.Self {
			continue
		}
		add("self record", Pass, fmt.Sprintf("%s (%s/%s, %s)", m.Name, m.OS, m.Arch, orDefault(m.TailscaleIP, "no ip")), "")
		if m.SSHHostKey == "" {
			add("sshd", Warn, "no sshd host key in /etc/ssh, so other machines can't reach this one",
				"turn on sshd (Remote Login on macOS), then `yonder refresh "+m.Name+"`")
		}
		if stale(m, e.Now) {
			add("facts", Warn, "discovered "+since(m.DiscoveredAt, e.Now), "yonder refresh "+m.Name)
		}
		return true
	}
	if t, ok := e.Store.Tombstone(e.Self); ok {
		add("self record", Fail, fmt.Sprintf("%s was removed from the fleet on %s", e.Self, t.RemovedAt.Format("2006-01-02")),
			"`yonder add "+e.Self+"` from another machine re-adds it; `yonder remove --forget-local` clears this copy")
		return false
	}
	add("self record", Fail, "config says this machine is "+e.Self+" but there is no record for it", "yonder init")
	return false
}

func stale(m registry.Machine, now time.Time) bool {
	return m.DiscoveredAt.IsZero() || now.Sub(m.DiscoveredAt) > StaleAfter
}

func since(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// factsOnly marks a remote report's harnesses detected only when the machine's
// facts list them. the remote's own detection also counts config dirs, which a
// stray folder can trip. no facts yet means trust the remote.
func factsOnly(r agents.Report, m registry.Machine) agents.Report {
	if m.DiscoveredAt.IsZero() {
		return r
	}
	have := map[string]bool{}
	for _, h := range m.Facts.Harnesses {
		have[h.Name] = true
	}
	hs := make([]agents.HarnessStatus, len(r.Harnesses))
	for i, h := range r.Harnesses {
		h.Detected = h.Detected && have[h.Name]
		hs[i] = h
	}
	r.Harnesses = hs
	return r
}

// skillChecks turns an agents status into one check per detected harness.
func skillChecks(group string, r agents.Report, home, fix string) []Check {
	var out []Check
	for _, h := range r.Harnesses {
		if !h.Detected {
			continue
		}
		name := "skill: " + h.Name
		path := agents.Tilde(h.Path, home)
		switch h.State {
		case agents.StateCurrent:
			out = append(out, Check{Group: group, Name: name, Status: Pass, Detail: "current at " + path})
		case agents.StateMissing:
			out = append(out, Check{Group: group, Name: name, Status: Warn, Detail: "not installed, so " + h.Name + " doesn't know about yonder", Fix: fix})
		case agents.StateOutdated:
			d := "outdated at " + path
			if h.Installed != "" {
				d += " (from yonder " + h.Installed + ")"
			}
			out = append(out, Check{Group: group, Name: name, Status: Warn, Detail: d, Fix: fix})
		default:
			out = append(out, Check{Group: group, Name: name, Status: Warn, Detail: path + " is a symlink or file, so yonder leaves it alone",
				Fix: "remove " + path + ", then " + fix})
		}
	}
	if len(out) == 0 {
		out = append(out, Check{Group: group, Name: "skill", Status: Pass, Detail: "no agent harness found, nothing to install"})
	}
	return out
}

type lostMachine struct {
	name   string
	keys   []string
	shared []string // keys a live record also lists
	users  []string // the machines that list them
}

func lostKeys(ts []registry.Tombstone, live []registry.Machine) []lostMachine {
	var out []lostMachine
	for _, t := range ts {
		if t.Lost && len(t.SSHClientKeys) > 0 {
			_, shared, users := remote.SplitShared(t.SSHClientKeys, live)
			var names []string
			for _, ns := range users {
				for _, n := range ns {
					if !slices.Contains(names, n) {
						names = append(names, n)
					}
				}
			}
			slices.Sort(names)
			out = append(out, lostMachine{t.Name, t.SSHClientKeys, shared, names})
		}
	}
	return out
}

// symlinkedKeys reports whether ~/.ssh/authorized_keys or ~/.ssh is a symlink, and the real file.
func symlinkedKeys(home string) (string, bool) {
	path := transport.AuthorizedKeysPath(home)
	for _, p := range []string{path, filepath.Dir(path)} {
		if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
			real, err := filepath.EvalSymlinks(path)
			if err != nil {
				real = path
			}
			return real, true
		}
	}
	return "", false
}

func keyChecks(group, content string, lost []lostMachine) []Check {
	var out []Check
	for _, l := range lost {
		matched := transport.MatchingKeys(content, l.keys)
		shared := len(transport.MatchingKeys(strings.Join(matched, "\n"), l.shared))
		if n := len(matched) - shared; n > 0 {
			out = append(out, Check{Group: group, Name: "authorized_keys", Status: Fail,
				Detail: fmt.Sprintf("still trusts %d key(s) of lost machine %s", n, l.name),
				Fix:    "yonder remove " + l.name + " --lost --yes"})
		}
		if shared > 0 {
			who := strings.Join(l.users, ", ")
			out = append(out, Check{Group: group, Name: "authorized_keys", Status: Fail,
				Detail: fmt.Sprintf("still trusts %d key(s) lost machine %s shares with %s", shared, l.name, who),
				Fix:    "make new keys on " + who + ", then `yonder remove " + l.name + " --lost --include-shared --yes`"})
		}
	}
	if len(out) == 0 {
		names := make([]string, len(lost))
		for i, l := range lost {
			names[i] = l.name
		}
		out = append(out, Check{Group: group, Name: "authorized_keys", Status: Pass, Detail: "no keys of lost machines (" + strings.Join(names, ", ") + ")"})
	}
	return out
}

type peerResult struct {
	checks []Check
	names  []string // its registry, nil if not read
}

func fleetChecks(e Env, ms []registry.Machine, fl fleet) []Check {
	var peers []registry.Machine
	for _, m := range ms {
		if m.Name != e.Self {
			peers = append(peers, m)
		}
	}
	if len(peers) == 0 {
		return []Check{{Group: "fleet", Name: "fleet", Status: Pass, Detail: "only this machine so far", Fix: "yonder add <machine>"}}
	}
	results := make([]peerResult, len(peers))
	var wg sync.WaitGroup
	for i, m := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = peerChecks(e, m, fl)
		}()
	}
	wg.Wait()

	var out []Check
	for _, r := range results {
		out = append(out, r.checks...)
	}
	out = append(out, consistency(ms, peers, results))
	return out
}

func peerChecks(e Env, m registry.Machine, fl fleet) peerResult {
	var res peerResult
	add := func(name string, st Status, detail, fix string) {
		res.checks = append(res.checks, Check{Group: m.Name, Name: name, Status: st, Detail: detail, Fix: fix})
	}
	p, reach := e.Status.Reach(m.Name, m.DNSName, m.TailscaleIP)
	if reach == tailnet.NotInTailnet {
		add("tailnet", Fail, m.Name+" is not in tailscale status (renamed, or removed from the tailnet?)",
			"`yonder remove "+m.Name+"` if it's gone; `tailscale status` lists the tailnet")
		return res
	}
	if reach == tailnet.Offline {
		d := "offline, last seen " + lastSeen(p.LastSeen, e.Now)
		if m.Notes != "" {
			d += " — notes: " + m.Notes
		}
		if m.Pending {
			d += "; setup unfinished"
		}
		add("online", Warn, d, "")
		return res
	}
	add("online", Pass, "online at "+p.IP(), "")
	if m.Pending {
		if m.SSHHostKey == "" {
			add("setup", Warn, m.Name+" is online now; run `yonder add "+m.Name+"` to finish setup", "yonder add "+m.Name)
		} else {
			add("setup", Warn, "setup unfinished (it was offline when added)", "yonder refresh "+m.Name)
		}
		return res
	}
	if m.SSHHostKey == "" {
		add("setup", Fail, "no pinned ssh host key", "yonder add "+m.Name)
		return res
	}
	if reach == tailnet.AddressChanged {
		add("address", Fail, fmt.Sprintf("address changed: record has %s, tailnet has %s", m.TailscaleIP, p.IP()),
			"yonder remove "+m.Name+" && yonder add "+m.Name)
		return res
	}

	v, err := e.Remote.Version(m)
	if ue, ok := transport.IsUnreachable(err); ok {
		add("ssh", Fail, ue.Detail, sshFix(m, ue))
		return res
	}
	add("ssh", Pass, fmt.Sprintf("%s@%s, host key pinned", m.SSHUser, m.TailscaleIP), "")
	var ce *YonderError
	switch {
	case errors.As(err, &ce):
		add("yonder", Fail, fmt.Sprintf("%s didn't run (%v)", orDefault(m.YonderPath, "yonder"), ce), "yonder add "+m.Name+"  (finds or copies yonder again)")
		return res
	case err != nil:
		add("yonder", Fail, err.Error(), "")
		return res
	case v != e.Version:
		add("version", Warn, fmt.Sprintf("%s runs yonder %s, this machine runs %s", m.Name, v, e.Version),
			"install the same yonder version on both (brew upgrade yonder, or go install ...@latest)")
	default:
		add("version", Pass, "yonder "+v+", same as here", "")
	}

	if stale(m, e.Now) {
		add("facts", Warn, "discovered "+since(m.DiscoveredAt, e.Now), "yonder refresh "+m.Name)
	} else {
		add("facts", Pass, "discovered "+since(m.DiscoveredAt, e.Now), "")
	}

	if b, err := e.Remote.Registry(m); err != nil {
		add("registry", Warn, "can't read its registry: "+err.Error(), "")
	} else {
		res.names = []string{}
		for _, r := range b.Machines {
			res.names = append(res.names, r.Name)
		}
	}

	if r, err := e.Remote.Skills(m); err != nil {
		add("skill", Warn, "can't read skill status (an older yonder?): "+err.Error(), "upgrade yonder on "+m.Name)
	} else {
		res.checks = append(res.checks, skillChecks(m.Name, factsOnly(r, m), "", "yonder agents install --all-machines")...)
	}

	if keys, err := e.Remote.AuthorizedKeys(m); err != nil {
		add("authorized_keys", Warn, "can't read it: "+err.Error(), "")
	} else {
		if len(fl.lost) > 0 {
			res.checks = append(res.checks, keyChecks(m.Name, keys, fl.lost)...)
		}
		res.checks = append(res.checks, meshChecks(m.Name, m.Name, keys, fl)...)
	}
	return res
}

func sshFix(m registry.Machine, ue *transport.UnreachableError) string {
	d := ue.Detail
	switch {
	case ue.AuthFailed():
		port := m.SSHPort
		if port == 0 {
			port = 22
		}
		return fmt.Sprintf("ssh-copy-id -p %d %s@%s, or if you reach %s with a specific key: yonder ssh-key %s <key>", port, m.SSHUser, m.TailscaleIP, m.Name, m.Name)
	case strings.Contains(d, "Host key verification failed") || strings.Contains(d, "IDENTIFICATION"):
		return "its ssh host key changed. If you reinstalled " + m.Name + ", `yonder add " + m.Name + "` pins the new key; if not, find out why first"
	}
	return "check that sshd (Remote Login on macOS) is on at " + m.Name + " and that port " + fmt.Sprint(orPort(m.SSHPort)) + " is open"
}

func orPort(p int) int {
	if p == 0 {
		return 22
	}
	return p
}

// consistency compares the set of machine names here with every peer that answered.
func consistency(local, peers []registry.Machine, results []peerResult) Check {
	want := map[string]bool{}
	for _, m := range local {
		want[m.Name] = true
	}
	var diffs []string
	checked := 0
	for i, r := range results {
		if r.names == nil {
			continue
		}
		checked++
		have := map[string]bool{}
		for _, n := range r.names {
			have[n] = true
		}
		var missing, extra []string
		for n := range want {
			if !have[n] {
				missing = append(missing, n)
			}
		}
		for n := range have {
			if !want[n] {
				extra = append(extra, n)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			diffs = append(diffs, fmt.Sprintf("%s lacks %s", peers[i].Name, strings.Join(missing, ", ")))
		}
		if len(extra) > 0 {
			diffs = append(diffs, fmt.Sprintf("%s also has %s", peers[i].Name, strings.Join(extra, ", ")))
		}
	}
	c := Check{Group: "fleet", Name: "registry"}
	switch {
	case checked == 0:
		c.Status, c.Detail = Warn, "no online machine to compare with"
	case len(diffs) > 0:
		c.Status, c.Detail, c.Fix = Warn, "registries differ: "+strings.Join(diffs, "; "), "yonder refresh"
	default:
		c.Status, c.Detail = Pass, fmt.Sprintf("same %d machines here and on %d online machine(s)", len(local), checked)
	}
	return c
}

func lastSeen(t, now time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return since(t, now)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Render prints the report grouped by machine, one line per check and its fix below.
func Render(w io.Writer, r Report, color bool) {
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
	label := map[Status]string{Pass: paint("32", "PASS"), Warn: paint("33", "WARN"), Fail: paint("31;1", "FAIL")}
	width := 0
	for _, c := range r.Checks {
		width = max(width, len(c.Name))
	}
	head := "yonder doctor"
	if r.Self != "" {
		head += " on " + r.Self
	}
	fmt.Fprintf(w, "%s (yonder %s)\n", paint("1", head), r.Version)
	group := ""
	for _, c := range r.Checks {
		if c.Group != group {
			group = c.Group
			title := group
			if group == groupLocal && r.Self != "" {
				title += " (" + r.Self + ")"
			}
			fmt.Fprintf(w, "\n%s\n", paint("1", title))
		}
		fmt.Fprintf(w, "  %s  %-*s  %s\n", label[c.Status], width, c.Name, c.Detail)
		if c.Fix != "" && c.Status != Pass {
			fmt.Fprintf(w, "        %-*s  %s %s\n", width, "", paint("2", "fix:"), c.Fix)
		}
	}
	s := r.Summary
	fmt.Fprintln(w)
	switch {
	case s.Fail > 0:
		fmt.Fprintf(w, "%s, %s, %d passed. Start with the FAIL lines.\n", plural(s.Fail, "failed", "failed"), plural(s.Warn, "warning", "warnings"), s.Pass)
	case s.Warn > 0:
		fmt.Fprintf(w, "Nothing broken. %s, %d passed.\n", plural(s.Warn, "warning", "warnings"), s.Pass)
	default:
		fmt.Fprintf(w, "All %d checks passed.\n", s.Pass)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// LocalHome is where doctor reads skill folders and authorized_keys.
func LocalHome() string { return registry.UserHome() }

// AuthorizedKeysScript prints ~/.ssh/authorized_keys on a remote, or nothing.
const AuthorizedKeysScript = `cat "$HOME/.ssh/authorized_keys" 2>/dev/null; exit 0`
