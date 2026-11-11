package transport

import (
	"os"
	"path/filepath"
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
