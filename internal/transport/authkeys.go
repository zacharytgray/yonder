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

// SymlinkError is an edit refused because authorized_keys (or ~/.ssh) is a
// symlink. Lines are the exact lines to delete by hand in Real; with Add, each
// is "+ line" to add or "- line" to delete.
type SymlinkError struct {
	Path, Real string
	Lines      []string
	Add        bool
}

func (e *SymlinkError) Error() string {
	var b strings.Builder
	if e.Add {
		fmt.Fprintf(&b, "%s is a symlink to %s, so yonder won't rewrite it. Make these %d change(s) to %s by hand (+ add the line, - delete it):", e.Path, e.Real, len(e.Lines), e.Real)
	} else {
		fmt.Fprintf(&b, "%s is a symlink to %s, so yonder won't rewrite it. Delete these %d line(s) from %s by hand:", e.Path, e.Real, len(e.Lines), e.Real)
	}
	for _, l := range e.Lines {
		b.WriteString("\n    " + l)
	}
	return b.String()
}

// symlinked reports whether path or its directory is a symlink.
func symlinked(path string) bool {
	for _, p := range []string{path, filepath.Dir(path)} {
		if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// StripAuthorizedKeys removes the lines whose key is in keys and keeps every
// other line as is. the original goes to authorized_keys.yonder-bak.<unix-nanos>
// first, a new one per change. a missing
// file is not an error. returns how many lines it removed.
// it never writes through a symlink: that is a *SymlinkError naming the lines.
func StripAuthorizedKeys(path string, keys []string) (removed int, backup string, err error) {
	set := keySet(keys)
	if len(set) == 0 {
		return 0, "", nil
	}
	lines, backup, err := EditAuthorizedKeys(path, false, func(content string) (string, []string) {
		var kept strings.Builder
		var gone []string
		for _, line := range strings.SplitAfter(content, "\n") {
			if id, ok := KeyID(line); ok && set[id] {
				gone = append(gone, strings.TrimRight(line, "\r\n"))
				continue
			}
			kept.WriteString(line)
		}
		return kept.String(), gone
	})
	return len(lines), backup, err
}

// EditAuthorizedKeys rewrites path with edit, which returns the new content and
// the lines it removed or added. no lines leaves the file alone. otherwise the
// original goes to authorized_keys.yonder-bak.<unix-nanos> first (never
// overwriting an earlier backup), and the new file keeps its mode.
// a missing file is empty with create (made 0600, its folder 0700), else nothing to do.
// it never writes through a symlink: that is a *SymlinkError naming the lines.
func EditAuthorizedKeys(path string, create bool, edit func(content string) (string, []string)) (lines []string, backup string, err error) {
	data, err := os.ReadFile(path)
	exists := err == nil
	switch {
	case errors.Is(err, os.ErrNotExist) && !create:
		return nil, "", nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return nil, "", err
	}
	if !exists && symlinked(path) { // a dangling link, or a linked ~/.ssh
		data = nil
	}
	content, lines := edit(string(data))
	if len(lines) == 0 {
		return nil, "", nil
	}
	if symlinked(path) {
		real, rerr := filepath.EvalSymlinks(path)
		if rerr != nil {
			real = path
		}
		return nil, "", &SymlinkError{Path: path, Real: real, Lines: lines, Add: create}
	}
	mode := os.FileMode(0o600)
	if exists {
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
			return nil, "", err
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, "", err
	}
	if err := writeAtomic(path, []byte(content), mode); err != nil {
		return nil, backup, err
	}
	return lines, backup, nil
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
