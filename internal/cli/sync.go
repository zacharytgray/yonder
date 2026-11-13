package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/store"
	"github.com/zacharytgray/yonder/internal/transport"
)

type syncOpts struct {
	background bool
	dryRun     bool
	resolve    string
	keep       string
	deletes    bool // let a scan delete most of a store
	staleOK    bool // take files a long-absent machine brings back
	quiet      bool // only problems: for syncs that ride on another command
}

func newSyncCmd() *cobra.Command {
	var o syncOpts
	cmd := &cobra.Command{
		Use:   "sync [store] [machine...] [--background] [--dry-run] [--resolve <path> --keep mine|theirs|<machine>]",
		Short: "Sync the registry and the stores of the layers that are on",
		Long: `Sync the registry with every online machine, then each store whose layer is on
(agents), peer to peer over ssh. Name a store to sync only it, and machines to sync
only with them.

Each file carries a version counter per machine. A change one side has seen all of
wins; two edits neither side has seen are both kept: the later edit stays at the path,
the other goes beside it as <name>.conflict-<machine>-<yyyymmdd>.<ext>. Settle one with
--resolve <path> --keep mine|theirs|<machine>, or delete the copy. Replaced and deleted
versions are kept (` + "`yonder agents restore`" + `). Files that can't sync are FAIL lines.

A scan that would delete two or more files that are more than half of a store stops: a
moved or missing folder looks like that. --allow-deletes goes ahead. Two machines that
haven't synced with each other for over 90 days hold back the old files only one of them
has, since the other may have deleted them meanwhile; --stale-ok syncs them, and clears every
hold this machine keeps, for every machine.

There is no daemon. --background starts the sync and returns at once, for a
session-start hook. --dry-run shows what would move and changes nothing here (the
other machines still record their own edits).`,
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			if (o.resolve == "") != (o.keep == "") {
				return &usageError{errors.New("--resolve and --keep go together")}
			}
			var only string
			if len(args) > 0 {
				if _, ok := store.Lookup(args[0]); ok {
					only, args = args[0], args[1:]
				}
			}
			if o.background {
				return a.syncBackground(only, args)
			}
			return a.syncCommand(only, args, o)
		},
	}
	cmd.Flags().BoolVar(&o.background, "background", false, "start the sync in the background and return at once")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "show what would move; change nothing here")
	cmd.Flags().StringVar(&o.resolve, "resolve", "", "settle the conflict at this store path")
	cmd.Flags().StringVar(&o.keep, "keep", "", "with --resolve: mine, theirs, or a machine name")
	cmd.Flags().BoolVar(&o.deletes, "allow-deletes", false, "let this sync delete most or all of a store here (it stops otherwise)")
	cmd.Flags().BoolVar(&o.staleOK, "stale-ok", false, "sync held-back files and clear every hold this machine keeps, for all machines")
	return cmd
}

// storesOn are the registered stores whose layer is on here.
func (a *app) storesOn() []string {
	var out []string
	for _, n := range store.Names() {
		k, _ := store.Lookup(n)
		if a.store.LayerOn(k.Layer) {
			out = append(out, n)
		}
	}
	return out
}

func (a *app) syncCommand(only string, machines []string, o syncOpts) error {
	self, err := a.selfName()
	if err != nil {
		return err
	}
	for _, m := range machines {
		if _, err := a.machine(m); err != nil {
			return err
		}
	}
	if o.resolve != "" {
		if only == "" {
			only = registry.LayerAgents
		}
		if err := a.resolve(only, self, o.resolve, o.keep); err != nil {
			return err
		}
	}
	if !o.dryRun {
		a.syncFleet(self)
		a.applyLayers(self)
	}
	stores := a.storesOn()
	if only != "" {
		k, _ := store.Lookup(only)
		if !slices.Contains(stores, only) {
			return layerOff(k.Layer, "syncing the "+only+" store")
		}
		stores = []string{only}
	}
	failed := false
	for _, name := range stores {
		if !a.syncStore(name, self, machines, o) {
			failed = true
		}
	}
	if failed {
		return quietExit(ExitError)
	}
	return nil
}

func (a *app) resolve(name, self, rel, keep string) error {
	s, err := store.Open(name, self)
	if err != nil {
		return err
	}
	rel = storeRel(s, rel)
	if err := s.Lock(30 * time.Second); err != nil {
		return err
	}
	defer s.Unlock()
	if _, _, err := s.Scan(); err != nil {
		return err
	}
	s.Harness = detectHarness()
	kept, removed, err := s.Resolve(rel, keep)
	if err != nil {
		return err
	}
	if _, _, err := s.Scan(); err != nil {
		return err
	}
	if err := s.Save(); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s: kept %s's version of %s; removed %s\n", name, kept, rel, strings.Join(removed, ", "))
	return nil
}

// syncStoresQuiet syncs every store that is on, printing only what moved and
// what failed. for commands that sync the registry anyway (refresh, add).
func (a *app) syncStoresQuiet(self string) {
	for _, name := range a.storesOn() {
		a.syncStore(name, self, nil, syncOpts{quiet: true})
	}
}

// storeRel accepts a path inside the store folder, absolute or relative to it.
func storeRel(s *store.Store, p string) string {
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(s.Dir, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// syncStore syncs one store with the online peers (or the named ones), then
// once more with any peer synced before this machine got something new, so
// changes are relayed between peers. false when something failed.
func (a *app) syncStore(name, self string, only []string, o syncOpts) bool {
	s, err := store.Open(name, self)
	if err != nil {
		a.warnf("%v", err)
		return false
	}
	wait := 30 * time.Second
	if o.quiet {
		wait = 2 * time.Second
	}
	if err := s.Lock(wait); err != nil {
		a.warnf("%s: %v", name, err)
		return errors.Is(err, store.ErrBusy) && o.quiet // a sync already running is fine for a ride-along
	}
	defer s.Unlock()
	s.AllowDeletes, s.StaleOK = o.deletes, o.staleOK
	scanned, refused, err := s.Scan()
	if err != nil {
		a.warnf("%s: %v", name, err)
		return false
	}
	ok := len(refused) == 0
	for _, r := range refused {
		fmt.Fprintf(a.out, "%s: FAIL %s\n", name, r)
	}
	if !o.dryRun {
		if err := s.Save(); err != nil {
			a.warnf("%s: %v", name, err)
			return false
		}
		// edits made here re-render this machine's instructions too
		if len(scanned) > 0 && s.Kind.AfterChange != nil {
			s.Kind.AfterChange(s, scanned)
		}
	}
	peers, err := a.onlinePeers(self)
	if err != nil {
		a.warnf("%s: sync skipped: %v", name, err)
		return false
	}
	if len(only) > 0 {
		peers = slices.DeleteFunc(peers, func(m registry.Machine) bool { return !slices.Contains(only, m.Name) })
		for _, n := range only {
			if !slices.ContainsFunc(peers, func(m registry.Machine) bool { return m.Name == n }) {
				fmt.Fprintf(a.out, "%s: %s skipped: offline, pending or moved\n", name, n)
			}
		}
	}
	if len(peers) == 0 && !o.quiet {
		fmt.Fprintf(a.out, "%s: no other machine is online\n", name)
	}
	gotAt := map[string]int{} // how many changes this machine had received when it synced each peer
	failed := map[string]bool{}
	received := 0
	for pass := 0; pass < 2; pass++ {
		for _, m := range peers {
			if pass == 1 && (failed[m.Name] || gotAt[m.Name] == received) {
				continue
			}
			rep, err := s.SyncWith(&sshPeer{a: a, m: m, store: name}, o.dryRun)
			if err != nil {
				var off storeOffError
				var busy storeBusyError
				var held storeHeldError
				switch {
				case errors.As(err, &off):
					if !o.quiet {
						fmt.Fprintf(a.out, "%s: %s skipped: the %s layer is off there\n", name, m.Name, s.Kind.Layer)
					}
				case errors.As(err, &busy):
					fmt.Fprintf(a.out, "%s: %s skipped: it is busy syncing; it catches up at the next sync\n", name, m.Name)
				case errors.As(err, &held):
					fmt.Fprintf(a.out, "%s: %s held: mass delete on %s (%s); check there, then `yonder sync --allow-deletes` there if it was meant\n", name, m.Name, m.Name, held.msg)
				default:
					fmt.Fprintf(a.out, "%s: %s failed: %v\n", name, m.Name, err)
					ok = false
				}
				failed[m.Name] = true
				continue
			}
			received += len(rep.Got) + len(rep.Conflicts)
			gotAt[m.Name] = received
			if !a.printReport(name, rep, o.quiet) {
				ok = false
			}
		}
		if o.dryRun {
			break
		}
	}
	return ok
}

// printReport prints one peer's sync. false when it holds a refusal.
func (a *app) printReport(name string, r store.Report, quiet bool) bool {
	ok := len(r.PeerRefused) == 0
	verb := ""
	if r.DryRun {
		verb = "would "
	}
	if !quiet || r.Changed() {
		if !r.Changed() && len(r.Skipped) == 0 {
			fmt.Fprintf(a.out, "%s: %s: up to date\n", name, r.Peer)
		} else {
			fmt.Fprintf(a.out, "%s: %s: %sget %d, %ssend %d\n", name, r.Peer, verb, len(r.Got), verb, len(r.Sent))
		}
	}
	lines := 0
	line := func(format string, args ...any) {
		lines++
		if lines <= 20 {
			fmt.Fprintf(a.out, "  "+format+"\n", args...)
		}
	}
	for _, g := range r.Got {
		what := "got"
		switch g.Action {
		case "deleted":
			what = "deleted"
		case "pending":
			what = "waits for review:"
		}
		line("%s %s (%s)", what, agents.Printable(g.Path), originText(g.State.Origin))
	}
	if !quiet {
		for _, p := range r.Sent {
			if !slices.Contains(r.PeerPending, p) {
				line("sent %s", p)
			}
		}
	}
	for _, p := range r.PeerPending {
		line("sent %s; it waits for review on %s (`yonder agents accept` there)", p, r.Peer)
	}
	for _, c := range r.Conflicts {
		line("conflict at %s: kept %s's edit; %s's is at %s (yonder sync --resolve %s --keep mine|theirs)", c.Path, c.Winner, c.Loser, c.Copy, c.Path)
	}
	for _, p := range r.DeleteLost {
		line("%s was deleted on one machine and edited on the other; the edit was kept", p)
	}
	for _, sk := range r.Skipped {
		line("skipped %s: %s", sk.Path, sk.Why)
	}
	if lines > 20 {
		fmt.Fprintf(a.out, "  and %d more (yonder agents log)\n", lines-20)
	}
	for _, rf := range r.PeerRefused {
		fmt.Fprintf(a.out, "%s: FAIL on %s: %s\n", name, r.Peer, rf)
	}
	return ok
}

// originText says where a version came from. a peer writes these fields.
func originText(o store.Origin) string {
	s := "from " + orDash(agents.Printable(o.Machine))
	if o.Harness != "" {
		s += " via " + agents.Printable(o.Harness)
	}
	if !o.Time.IsZero() {
		s += ", " + ago(o.Time)
	}
	return s
}

// syncBackground starts `yonder sync` in its own session and returns. its
// output goes to <state>/sync/last.log.
func (a *app) syncBackground(only string, machines []string) error {
	if _, err := a.selfName(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Join(registry.StateDir(), "sync")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "last.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	args := []string{"sync"}
	if only != "" {
		args = append(args, only)
	}
	args = append(args, machines...)
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// sshPeer is the other side of a store sync, over the pinned ssh transport.
type sshPeer struct {
	a     *app
	m     registry.Machine
	store string
}

type storeOffError struct{ msg string }

func (e storeOffError) Error() string { return e.msg }

type storeBusyError struct{ msg string }

func (e storeBusyError) Error() string { return e.msg }

type storeHeldError struct{ msg string }

func (e storeHeldError) Error() string { return e.msg }

func (p *sshPeer) Name() string { return p.m.Name }

func (p *sshPeer) call(cmd string, stdin []byte, out *bytes.Buffer) error {
	var errb bytes.Buffer
	code, err := p.a.ssh.Yonder(p.a.ctx, p.a.target(p.m), []string{cmd, p.store}, transport.IO{
		Stdin: bytes.NewReader(stdin), Stdout: out, Stderr: &errb,
	})
	if ue, ok := transport.IsUnreachable(err); ok {
		return unreachableError(p.m, ue)
	}
	if err != nil {
		return err
	}
	msg := strings.TrimSpace(errb.String())
	switch {
	case code == 0:
		return nil
	case code == remote.ExitStoreOff:
		return storeOffError{msg}
	case code == remote.ExitStoreBusy:
		return storeBusyError{msg}
	case code == remote.ExitStoreHeld:
		return storeHeldError{heldWhy(msg)}
	case code == ExitUsage && strings.Contains(msg, "unknown"):
		return fmt.Errorf("%s is too old for store sync; upgrade yonder there", p.m.Name)
	}
	return fmt.Errorf("%s exited %d: %s", cmd, code, lastLine(msg))
}

func (p *sshPeer) Offer() (store.Manifest, error) {
	var out bytes.Buffer
	if err := p.call("_store-offer", nil, &out); err != nil {
		return store.Manifest{}, err
	}
	var m store.Manifest
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		return m, fmt.Errorf("bad _store-offer reply: %w", err)
	}
	return m, nil
}

func (p *sshPeer) Fetch(paths []string) (store.Bundle, error) {
	req, _ := json.Marshal(map[string][]string{"paths": paths})
	var out bytes.Buffer
	if err := p.call("_store-send", req, &out); err != nil {
		return store.Bundle{}, err
	}
	return store.ReadBundle(&out)
}

func (p *sshPeer) Push(b store.Bundle) (store.PushResult, error) {
	var in, out bytes.Buffer
	if err := store.WriteBundle(&in, b); err != nil {
		return store.PushResult{}, err
	}
	if err := p.call("_store-recv", in.Bytes(), &out); err != nil {
		return store.PushResult{}, err
	}
	var r store.PushResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		return r, fmt.Errorf("bad _store-recv reply: %w", err)
	}
	return r, nil
}

var heldCounts = regexp.MustCompile(`(\d+) of the (\d+) files`)

// heldWhy is what a peer's mass-delete error found, without its fix.
func heldWhy(msg string) string {
	if m := heldCounts.FindStringSubmatch(msg); m != nil {
		return m[1] + " of its " + m[2] + " files are gone"
	}
	if strings.Contains(msg, "is missing") {
		return "its store folder is missing"
	}
	return lastLine(msg)
}
