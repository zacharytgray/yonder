package transport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// KeyID is the "type base64" part of a public key line, without options or comment.
// ok is false for blank lines, comments and lines with no key.
func KeyID(line string) (id string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", false
	}
	f := fields(line)
	for i := 0; i+1 < len(f); i++ {
		if isKeyType(f[i]) {
			return f[i] + " " + f[i+1], true
		}
	}
	return "", false
}

func isKeyType(s string) bool {
	for _, p := range []string{"ssh-", "ecdsa-sha2-", "sk-ssh-", "sk-ecdsa-sha2-"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// fields splits on whitespace, keeping double-quoted option values whole.
func fields(s string) []string {
	var out []string
	var b strings.Builder
	quoted, esc := false, false
	for _, r := range s {
		switch {
		case esc:
			esc = false
		case r == '\\' && quoted:
			esc = true
		case r == '"':
			quoted = !quoted
		case !quoted && (r == ' ' || r == '\t'):
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func keySet(keys []string) map[string]bool {
	set := map[string]bool{}
	for _, k := range keys {
		if id, ok := KeyID(k); ok {
			set[id] = true
		}
	}
	return set
}

// MatchingKeys returns the lines of an authorized_keys file whose key is in keys.
func MatchingKeys(content string, keys []string) []string {
	set := keySet(keys)
	var out []string
	for _, line := range strings.Split(content, "\n") {
		if id, ok := KeyID(line); ok && set[id] {
			out = append(out, line)
		}
	}
	return out
}

// AuthorizedKeysPath is ~/.ssh/authorized_keys under home.
func AuthorizedKeysPath(home string) string {
	return filepath.Join(home, ".ssh", "authorized_keys")
}

// StripAuthorizedKeys removes the lines whose key is in keys and keeps every
// other line as is. the original goes to authorized_keys.yonder-bak.<unix-nanos>
// first, a new one per change. a missing
// file is not an error. returns how many lines it removed.
func StripAuthorizedKeys(path string, keys []string) (removed int, backup string, err error) {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	set := keySet(keys)
	if len(set) == 0 {
		return 0, "", nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	var kept strings.Builder
	for _, line := range lines {
		if id, ok := KeyID(line); ok && set[id] {
			removed++
			continue
		}
		kept.WriteString(line)
	}
	if removed == 0 {
		return 0, "", nil
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	for n := time.Now().UnixNano(); ; n++ { // never overwrite an earlier backup
		backup = filepath.Join(filepath.Dir(path), fmt.Sprintf("authorized_keys.yonder-bak.%d", n))
		if _, err := os.Lstat(backup); errors.Is(err, os.ErrNotExist) {
			break
		}
	}
	if err := writeAtomic(backup, data, mode); err != nil {
		return 0, "", err
	}
	if err := writeAtomic(path, []byte(kept.String()), mode); err != nil {
		return 0, backup, err
	}
	return removed, backup, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".yonder-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
