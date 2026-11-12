package remote

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/transport"
)

// lostAppliedPath lists the lost tombstones whose keys this machine already
// stripped, one "<name> <removed_at>" per line.
func lostAppliedPath() string { return filepath.Join(registry.StateDir(), "lost-keys-applied") }

func lostID(t registry.Tombstone) string {
	return t.Name + " " + t.RemovedAt.UTC().Format(time.RFC3339)
}

// LostKeys is what sync may strip for lost tombstone t: keys that are both in t
// and in the record this machine held for that name, minus any key a live
// record (self included) lists.
func LostKeys(t registry.Tombstone, live []registry.Machine) []string {
	held := map[string]bool{}
	for _, k := range t.HeldKeys {
		if id, ok := transport.KeyID(k); ok {
			held[id] = true
		}
	}
	for _, m := range live {
		for _, k := range m.SSHClientKeys {
			if id, ok := transport.KeyID(k); ok {
				delete(held, id)
			}
		}
	}
	var out []string
	for _, k := range t.SSHClientKeys {
		if id, ok := transport.KeyID(k); ok && held[id] {
			out = append(out, k)
		}
	}
	return out
}

// SplitShared splits a lost machine's keys into the ones no live record lists
// (only) and the ones some live record also lists (shared), with the names of
// the machines that list each shared key, keyed by KeyID.
// live must not include the lost machine itself.
func SplitShared(keys []string, live []registry.Machine) (only, shared []string, users map[string][]string) {
	users = map[string][]string{}
	for _, m := range live {
		for _, k := range m.SSHClientKeys {
			if id, ok := transport.KeyID(k); ok && !slices.Contains(users[id], m.Name) {
				users[id] = append(users[id], m.Name)
			}
		}
	}
	for _, k := range keys {
		id, ok := transport.KeyID(k)
		switch {
		case !ok:
		case len(users[id]) > 0:
			shared = append(shared, k)
		default:
			only = append(only, k)
		}
	}
	for id := range users {
		if !slices.ContainsFunc(shared, func(k string) bool { kid, _ := transport.KeyID(k); return kid == id }) {
			delete(users, id)
		}
	}
	return only, shared, users
}

// StripLost removes lost machines' keys (LostKeys) from this machine's
// authorized_keys, once per tombstone, so a sync doesn't rewrite the file
// every time. returns one line per tombstone it acted on.
func StripLost(ts []registry.Tombstone, live []registry.Machine) ([]string, error) {
	done := map[string]bool{}
	if data, err := os.ReadFile(lostAppliedPath()); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(data)))
		for sc.Scan() {
			done[strings.TrimSpace(sc.Text())] = true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var out, applied []string
	var errs []error
	path := transport.AuthorizedKeysPath(registry.UserHome())
	for _, t := range ts {
		keys := LostKeys(t, live)
		if !t.Lost || len(keys) == 0 || done[lostID(t)] {
			continue
		}
		n, _, err := transport.StripAuthorizedKeys(path, keys)
		if err != nil {
			errs = append(errs, fmt.Errorf("stripping %s's keys: %w", t.Name, err))
			continue
		}
		applied = append(applied, lostID(t))
		if n > 0 {
			out = append(out, fmt.Sprintf("removed %d line(s) of lost machine %s from ~/.ssh/authorized_keys", n, t.Name))
		}
	}
	if len(applied) > 0 {
		if err := appendLines(lostAppliedPath(), applied); err != nil {
			errs = append(errs, err)
		}
	}
	return out, errors.Join(errs...)
}

func appendLines(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
