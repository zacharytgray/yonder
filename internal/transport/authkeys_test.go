package transport

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	lostKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlost me@gpu-box"
	otherKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIother me@laptop"
)

func TestKeyID(t *testing.T) {
	cases := map[string]string{
		"ssh-ed25519 AAAAx me@gpu-box":                               "ssh-ed25519 AAAAx",
		"ssh-ed25519 AAAAx":                                          "ssh-ed25519 AAAAx",
		"restrict ssh-ed25519 AAAAx c":                               "ssh-ed25519 AAAAx",
		`command="echo ssh-rsa fake",no-pty ecdsa-sha2-nistp256 B c`: "ecdsa-sha2-nistp256 B",
		"  sk-ssh-ed25519@openssh.com AAAAs  ":                       "sk-ssh-ed25519@openssh.com AAAAs",
	}
	for in, want := range cases {
		if got, ok := KeyID(in); !ok || got != want {
			t.Errorf("KeyID(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "   ", "# ssh-ed25519 AAAAx", "garbage"} {
		if _, ok := KeyID(in); ok {
			t.Errorf("KeyID(%q) should not match", in)
		}
	}
}

func TestStripAuthorizedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	orig := "# my keys\n" +
		otherKey + "\n" +
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlost a-different-comment\n" +
		"\n" +
		`restrict,command="uptime" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlost` + "\n" +
		"restrict " + otherKey + "\n" +
		"ssh-rsa AAAAB3rsa backup\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	n, backup, err := StripAuthorizedKeys(path, []string{lostKey})
	if err != nil || n != 2 {
		t.Fatalf("removed %d, err %v", n, err)
	}
	got, _ := os.ReadFile(path)
	want := "# my keys\n" + otherKey + "\n\nrestrict " + otherKey + "\nssh-rsa AAAAB3rsa backup\n"
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if b, _ := os.ReadFile(backup); string(b) != orig {
		t.Fatalf("backup should hold the original:\n%s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed: %v", st.Mode())
	}
	// nothing left to strip: no rewrite, no new backup
	os.Remove(backup)
	if n, backup, err := StripAuthorizedKeys(path, []string{lostKey}); n != 0 || backup != "" || err != nil {
		t.Fatalf("second run: %d %q %v", n, backup, err)
	}
	if baks, _ := filepath.Glob(filepath.Join(dir, "authorized_keys.yonder-bak*")); len(baks) != 0 {
		t.Fatalf("no-op run should not write a backup: %v", baks)
	}
	if n, _, err := StripAuthorizedKeys(filepath.Join(dir, "missing"), []string{lostKey}); n != 0 || err != nil {
		t.Fatalf("missing file: %d %v", n, err)
	}
}

func TestMatchingKeys(t *testing.T) {
	content := otherKey + "\nrestrict " + lostKey + "\n"
	got := MatchingKeys(content, []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlost"})
	if len(got) != 1 || got[0] != "restrict "+lostKey {
		t.Fatalf("got %q", got)
	}
}

func TestStripKeepsEveryBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	orig := otherKey + "\n" + lostKey + "\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, _, err := StripAuthorizedKeys(path, []string{lostKey}); n != 1 || err != nil {
		t.Fatalf("first strip: %d %v", n, err)
	}
	if err := os.WriteFile(path, []byte(otherKey+"\n"+lostKey+"\n# later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, _, err := StripAuthorizedKeys(path, []string{otherKey}); n != 1 || err != nil {
		t.Fatalf("second strip: %d %v", n, err)
	}
	baks, _ := filepath.Glob(filepath.Join(dir, "authorized_keys.yonder-bak.*"))
	if len(baks) != 2 {
		t.Fatalf("want one backup per change, got %v", baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != orig {
		t.Fatalf("the first backup should still hold the original: %q", b)
	}
}

// a symlinked authorized_keys (or ~/.ssh) is never rewritten: the error names the real file and lines
func TestStripRefusesSymlinks(t *testing.T) {
	orig := otherKey + "\nrestrict " + lostKey + "\n"
	check := func(t *testing.T, path, real string) {
		t.Helper()
		n, backup, err := StripAuthorizedKeys(path, []string{lostKey})
		var se *SymlinkError
		if !errors.As(err, &se) || n != 0 || backup != "" {
			t.Fatalf("want a SymlinkError, got %d %q %v", n, backup, err)
		}
		if len(se.Lines) != 1 || se.Lines[0] != "restrict "+lostKey || !strings.Contains(err.Error(), "restrict "+lostKey) {
			t.Fatalf("lines %q, msg %q", se.Lines, err)
		}
		if want, _ := filepath.EvalSymlinks(real); se.Real != want {
			t.Fatalf("real path %q, want %q", se.Real, want)
		}
		if got, _ := os.ReadFile(real); string(got) != orig {
			t.Fatalf("target rewritten:\n%s", got)
		}
		if baks, _ := filepath.Glob(filepath.Join(filepath.Dir(real), "*yonder-bak*")); len(baks) != 0 {
			t.Fatalf("backup written next to the target: %v", baks)
		}
		// nothing matching: no error
		if n, _, err := StripAuthorizedKeys(path, []string{"ssh-ed25519 AAAAnotthere"}); n != 0 || err != nil {
			t.Fatalf("no match: %d %v", n, err)
		}
	}
	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "unrelated-file")
		if err := os.WriteFile(real, []byte(orig), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "authorized_keys")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		check(t, link, real)
	})
	t.Run("dir", func(t *testing.T) {
		dir := t.TempDir()
		realDir := filepath.Join(dir, "dotfiles-ssh")
		if err := os.Mkdir(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		real := filepath.Join(realDir, "authorized_keys")
		if err := os.WriteFile(real, []byte(orig), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realDir, filepath.Join(dir, ".ssh")); err != nil {
			t.Fatal(err)
		}
		check(t, filepath.Join(dir, ".ssh", "authorized_keys"), real)
	})
}

func TestEditAuthorizedKeysCreateAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".ssh", "authorized_keys")
	add := func(c string) (string, []string) { return c + otherKey + "\n", []string{otherKey} }
	if _, backup, err := EditAuthorizedKeys(path, true, add); err != nil || backup != "" {
		t.Fatalf("create: %q %v", backup, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("created %v %v", st, err)
	}
	if _, _, err := EditAuthorizedKeys(filepath.Join(dir, "none"), false, add); err != nil {
		t.Fatalf("missing without create: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	_, _, err := EditAuthorizedKeys(link, true, add)
	var se *SymlinkError
	if !errors.As(err, &se) || !se.Add || !strings.Contains(err.Error(), "Make these 1 change(s) to") {
		t.Fatalf("want an add refusal, got %v", err)
	}
}
