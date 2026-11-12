package mesh

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/transport"
)

// ssh's default identities, in the order ssh tries them
var defaultIdentities = []string{"id_rsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519", "id_ed25519_sk"}

// LocalKeys is the public keys ssh on this machine offers by default (the
// default identities under home/.ssh whose private key and .pub both exist),
// then the .pub of each private key in identities, deduped.
func LocalKeys(home string, identities []string) []string {
	var paths []string
	for _, n := range defaultIdentities {
		paths = append(paths, filepath.Join(home, ".ssh", n))
	}
	paths = append(paths, identities...)
	out := []string{}
	seen := map[string]bool{}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		data, err := os.ReadFile(p + ".pub")
		if err != nil {
			continue
		}
		line, _, _ := strings.Cut(string(data), "\n")
		line = strings.TrimSpace(line)
		if id, ok := transport.KeyID(line); ok && !seen[id] {
			seen[id] = true
			out = append(out, line)
		}
	}
	return out
}

// Own is this machine's mesh keys: its default identities plus every key set
// with `yonder ssh-key` here.
func Own(s *registry.Store) []string {
	var ids []string
	if c, err := s.Config(); err == nil {
		for _, t := range c.SSH {
			if t.Identity != "" {
				ids = append(ids, registry.ExpandHome(t.Identity))
			}
		}
	}
	slices.Sort(ids)
	return LocalKeys(registry.UserHome(), ids)
}

// Union is a followed by the keys of b that a lacks, by key.
func Union(a, b []string) []string {
	out := slices.Clone(a)
	if out == nil {
		out = []string{}
	}
	have := map[string]bool{}
	for _, k := range a {
		if id, ok := transport.KeyID(k); ok {
			have[id] = true
		}
	}
	for _, k := range b {
		if id, ok := transport.KeyID(k); ok && !have[id] {
			have[id] = true
			out = append(out, k)
		}
	}
	return out
}

// Clean keeps the keys in a peer's report that are safe to grant, as
// "type base64 [comment]" with a plain printable comment of at most 120 bytes.
// at most 32 keys.
func Clean(keys []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, k := range keys {
		id, ok := cleanKey(k)
		if !ok || seen[id] || len(out) == 32 {
			continue
		}
		seen[id] = true
		line := id
		if f := strings.Fields(k); len(f) > 0 {
			b64 := strings.Fields(id)[1]
			if i := slices.Index(f, b64); i >= 0 && i+1 < len(f) {
				c := strings.Join(f[i+1:], " ")
				if len(c) <= 120 && strings.IndexFunc(c, func(r rune) bool { return r < 0x20 || r > 0x7e }) < 0 {
					line += " " + c
				}
			}
		}
		out = append(out, line)
	}
	return out
}
