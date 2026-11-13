package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zacharytgray/yonder/internal/mesh"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/transport"
)

// onlinePeers returns the registered machines, other than self, that are set
// up, online and still at their pinned address.
func (a *app) onlinePeers(self string) ([]registry.Machine, error) {
	ms, err := a.store.Machines()
	if err != nil {
		return nil, err
	}
	if _, err := a.status(); err != nil {
		return nil, err
	}
	var out []registry.Machine
	for _, m := range ms {
		if m.Name == self || m.Pending || m.SSHHostKey == "" {
			continue
		}
		_, err := a.pinned(m)
		switch {
		case err == nil:
			out = append(out, m)
		case ExitCode(err) == ExitError: // moved, not just offline
			a.warnf("skipped %v", err)
		}
	}
	return out, nil
}

// syncFleet pushes the registry to every online machine and merges what comes back.
// a second pass catches machines that missed a newer record from a later peer.
// each receiver, and this machine, strips lost machines' keys once.
// returns the machines it reached.
func (a *app) syncFleet(self string) (reached []string) {
	defer a.stripLostHere()
	defer a.applyLayers(self) // the layers may have changed, and the briefing lists the records
	peers, err := a.onlinePeers(self)
	if err != nil {
		a.warnf("sync skipped: %v", err)
		return nil
	}
	if len(peers) == 0 {
		return nil
	}
	local, err := a.store.Bundle()
	if err != nil {
		a.warnf("sync skipped: %v", err)
		return nil
	}
	local.ClearLost = a.clearLost
	// each reply is merged as from that peer, under the same rules _sync uses
	seen := map[string]string{}
	for _, m := range peers {
		resp, err := a.pushBundle(m, local)
		if err != nil {
			a.warnf("sync with %s failed: %v", m.Name, err)
			continue
		}
		if _, err := a.store.MergeFrom(resp, m.Name); err != nil {
			a.warnf("sync: %v", err)
			return reached
		}
		seen[m.Name] = resp.Digest()
		reached = append(reached, m.Name)
	}
	merged, err := a.store.Bundle()
	if err != nil {
		a.warnf("sync: %v", err)
		return reached
	}
	merged.ClearLost = a.clearLost
	final := merged.Digest()
	for _, m := range peers {
		if d, ok := seen[m.Name]; ok && d != final {
			if _, err := a.pushBundle(m, merged); err != nil {
				a.warnf("sync with %s failed: %v", m.Name, err)
			}
		}
	}
	return reached
}

func (a *app) pushBundle(m registry.Machine, b registry.Bundle) (registry.Bundle, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return registry.Bundle{}, err
	}
	var out bytes.Buffer
	err = a.yonderJSON(a.ssh, m, []string{"_sync"}, data, &out)
	if err != nil {
		return registry.Bundle{}, err
	}
	var resp registry.Bundle
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		return registry.Bundle{}, fmt.Errorf("bad _sync reply: %w", err)
	}
	resp.ClearLost = nil // only a sender asks for that
	return resp, nil
}

// yonderJSON runs a hidden command and collects stdout; stderr goes into the error.
func (a *app) yonderJSON(r transport.Runner, m registry.Machine, args []string, stdin []byte, out *bytes.Buffer) error {
	var errb bytes.Buffer
	code, err := r.Yonder(a.ctx, a.target(m), args, transport.IO{
		Stdin: bytes.NewReader(stdin), Stdout: out, Stderr: &errb,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("yonder %s exited %d: %s", args[0], code, strings.TrimSpace(errb.String()))
	}
	return nil
}

// stripLostHere applies lost tombstones to this machine's authorized_keys, and drops
// the lines mesh added for any removed machine.
func (a *app) stripLostHere() {
	b, err := a.store.Bundle()
	if err != nil {
		a.warnf("lost keys: %v", err)
		return
	}
	lines, err := remote.StripLost(b.Tombstones, b.Machines)
	more, merr := mesh.StripRemoved(b.Tombstones, b.Machines)
	for _, l := range append(lines, more...) {
		fmt.Fprintln(a.out, l)
	}
	for _, e := range []error{err, merr} {
		if e != nil {
			a.warnf("%v", e)
		}
	}
}
