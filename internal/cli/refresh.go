package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/discover"
	"github.com/zacharytgray/yonder/internal/mesh"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/tailnet"
)

const lazyAfter = 24 * time.Hour

// lazyCap bounds how long ls and show wait for lazy refreshes. a var for tests.
var lazyCap = 5 * time.Second

func newRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh [machine]",
		Short: "Rediscover facts on online machines and sync the registry",
		Long: `Rerun discovery on every online machine (or one), update their records,
finish setup for pending machines that are now online and hold a pinned ssh host key,
and sync the registry everywhere. A pending machine with no pinned key is only reported:
` + "`yonder add <machine>`" + ` pins its key and finishes it.`,
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			self, err := a.selfName()
			if err != nil {
				return err
			}
			var ms []registry.Machine
			if len(args) == 1 {
				m, err := a.machine(args[0])
				if err != nil {
					return err
				}
				ms = []registry.Machine{m}
			} else if ms, err = a.store.Machines(); err != nil {
				return err
			}
			var lastErr error
			for _, m := range ms {
				if err := a.refreshOne(m, self); err != nil {
					if err.Error() != "" {
						a.warnf("%v", err)
					}
					lastErr = err
				}
			}
			a.syncFleet(self)
			a.applyLayers(self)
			a.syncStoresQuiet(self)
			if len(args) == 1 && lastErr != nil {
				return &exitError{code: ExitCode(lastErr)}
			}
			return nil
		},
	}
}

func (a *app) refreshOne(m registry.Machine, self string) error {
	st, stErr := a.status()
	if m.Name != self {
		if stErr != nil {
			return withCode(ExitTempErr, "%s: %v", m.Name, stErr)
		}
		p, r := st.Reach(m.Name, m.DNSName, m.TailscaleIP)
		switch r {
		case tailnet.Offline:
			fmt.Fprintf(a.out, "skipped %s: offline (last seen %s)%s\n", m.Name, ago(p.LastSeen), notesSuffix(m))
			return &exitError{code: ExitTempErr}
		case tailnet.NotInTailnet, tailnet.AddressChanged:
			return reachError(m, p, r)
		}
		if m.Pending {
			// no held pin: only an explicit local add may trust a first key
			if m.SSHHostKey == "" {
				fmt.Fprintln(a.out, onlineNowLine(m.Name))
				return nil
			}
			// refresh is not an add: no trusting another account's node here
			if !st.Owned(p) {
				return fmt.Errorf("%s (%s) is not owned by your tailscale account: %s; finish it with `yonder add %s --allow-shared` if you mean it", m.Name, p.DNSName, st.NotOwnedWhy(p), p.DNSName)
			}
			// a pin held from before is checked strictly, never replaced
			o := addOpts{user: m.SSHUser, port: m.SSHPort, role: m.Role, notes: m.Notes, pin: m.SSHHostKey}
			o = a.withDefaults(m.Name, o)
			done, err := a.addPeer(p, o)
			if err != nil {
				return fmt.Errorf("finishing setup of %s: %w", m.Name, err)
			}
			fmt.Fprintf(a.out, "finished setup of %s (%s/%s)\n", done.Name, done.OS, done.Arch)
			return nil
		}
		// no pinned key and not pending: never trust a new key silently
		if m.SSHHostKey == "" {
			return fmt.Errorf("%s has no pinned ssh host key; run `yonder add %s`", m.Name, m.Name)
		}
	}
	m, err := a.rediscover(m, self)
	if err != nil {
		return fmt.Errorf("refreshing %s: %w", m.Name, err)
	}
	if _, err := a.store.MergeIn(registry.Bundle{Machines: []registry.Machine{m}}); err != nil {
		return err
	}
	if m.Name == self {
		fmt.Fprintf(a.out, "refreshed %s (this machine)\n", m.Name)
	} else {
		fmt.Fprintf(a.out, "refreshed %s\n", m.Name)
	}
	return nil
}

// onlineNowLine is the next step for a pending record with no held pin.
func onlineNowLine(name string) string {
	return fmt.Sprintf("%s is online now; run `yonder add %s` to finish setup", name, name)
}

// finishCmd finishes a pending record: refresh checks a held pin strictly;
// without one, only an explicit add pins a first key.
func finishCmd(m registry.Machine) string {
	if m.SSHHostKey != "" {
		return "yonder refresh " + m.Name
	}
	return "yonder add " + m.Name
}

// rediscover reads fresh facts for a set-up machine (self locally, a peer
// with _facts) and returns its updated record without saving it.
func (a *app) rediscover(m registry.Machine, self string) (registry.Machine, error) {
	st, stErr := a.status()
	if m.Name == self {
		m.Facts = discover.Local()
		now := time.Now().UTC()
		m.DiscoveredAt, m.UpdatedAt = now, now
		if stErr == nil {
			m.TailscaleIP, m.DNSName = st.Self.IP(), st.Self.DNSName
		}
		if m.SSHHostKey == "" {
			m.SSHHostKey = localHostKey()
		}
		m.MeshKeys = mesh.Own(a.store)
		m.SSHClientKeys = mesh.Union(m.SSHClientKeys, m.MeshKeys)
		return m, nil
	}
	if stErr != nil {
		return m, stErr
	}
	p, err := a.pinned(m)
	if err != nil {
		return m, err
	}
	f, err := a.remoteFacts(a.ssh, m)
	if err != nil {
		return m, err
	}
	now := time.Now().UTC()
	m.Facts, m.DiscoveredAt, m.UpdatedAt = f, now, now
	m.DNSName = p.DNSName
	return m, nil
}

// lazyRefresh rediscovers, in parallel, the machines in ms whose facts are
// older than lazyAfter: self, and online set-up peers. it waits at most
// lazyCap; anything slower keeps its stored facts. results are saved here
// only, without a sync (the next refresh or add spreads them).
// reports whether any record changed.
func (a *app) lazyRefresh(self string, ms []registry.Machine) bool {
	if self == "" {
		return false
	}
	st, stErr := a.status()
	var todo []registry.Machine
	for _, m := range ms {
		if m.Pending || (!m.DiscoveredAt.IsZero() && time.Since(m.DiscoveredAt) < lazyAfter) {
			continue
		}
		if m.Name == self {
			todo = append(todo, m)
			continue
		}
		if stErr != nil || m.SSHHostKey == "" {
			continue
		}
		if _, r := st.Reach(m.Name, m.DNSName, m.TailscaleIP); r == tailnet.Reachable {
			todo = append(todo, m)
		}
	}
	if len(todo) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(a.ctx, lazyCap)
	defer cancel()
	sub := *a // same app, capped context; status is already cached
	sub.ctx = ctx
	ch := make(chan registry.Machine, len(todo))
	for _, m := range todo {
		go func() {
			fresh, err := sub.rediscover(m, self)
			if err != nil {
				fresh = registry.Machine{}
			}
			ch <- fresh
		}()
	}
	var got []registry.Machine
wait:
	for range todo {
		select {
		case m := <-ch:
			if m.Name != "" {
				got = append(got, m)
			}
		case <-ctx.Done():
			break wait
		}
	}
	if len(got) == 0 {
		return false
	}
	if _, err := a.store.MergeIn(registry.Bundle{Machines: got}); err != nil {
		a.warnf("saving refreshed facts: %v", err)
		return false
	}
	return true
}
