package mesh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zacharytgray/yonder/internal/transport"
)

// the scripts run for real through /bin/sh, against a temp home
func shHome(t *testing.T) (transport.Local, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return transport.Local{}, home
}

func appendEdit(gs ...Grant) func(string) (string, []string) {
	return func(c string) (string, []string) { return Append(c, gs) }
}

func backups(t *testing.T, home string) []string {
	t.Helper()
	b, _ := filepath.Glob(filepath.Join(home, ".ssh", "authorized_keys.yonder-bak.*"))
	return b
}

func TestEditCreatesThenBacksUp(t *testing.T) {
	r, home := shHome(t)
	ctx := context.Background()
	ak := filepath.Join(home, ".ssh", "authorized_keys")

	lines, backup, err := Edit(ctx, r, transport.Target{}, home, true, appendEdit(Grant{"laptop", id(laptopKey)}))
	if err != nil || len(lines) != 1 || backup != "" {
		t.Fatalf("first edit: %q %q %v", lines, backup, err)
	}
	for p, want := range map[string]os.FileMode{ak: 0o600, filepath.Dir(ak): 0o700} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != want {
			t.Fatalf("%s: %v %v", p, st.Mode(), err)
		}
	}

	hand := laptopKey + "\n# keep me\n"
	if err := os.WriteFile(ak, []byte(hand), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ak, 0o640); err != nil {
		t.Fatal(err)
	}
	lines, backup, err = Edit(ctx, r, transport.Target{}, home, true, appendEdit(Grant{"laptop", id(laptopKey)}, Grant{"gpu-box", id(gpuKey)}))
	if err != nil || len(lines) != 1 || !strings.HasSuffix(lines[0], "yonder:gpu-box") {
		t.Fatalf("second edit: %q %v", lines, err)
	}
	if b, _ := os.ReadFile(backup); string(b) != hand {
		t.Fatalf("backup %s holds %q", backup, b)
	}
	got, _ := os.ReadFile(ak)
	if string(got) != hand+id(gpuKey)+" yonder:gpu-box\n" {
		t.Fatalf("file:\n%s", got)
	}
	if st, _ := os.Stat(ak); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %v", st.Mode())
	}

	// nothing missing: no write, no new backup
	n := len(backups(t, home))
	if lines, _, err := Edit(ctx, r, transport.Target{}, home, true, appendEdit(Grant{"gpu-box", id(gpuKey)})); err != nil || lines != nil {
		t.Fatalf("idempotent edit: %q %v", lines, err)
	}
	if len(backups(t, home)) != n {
		t.Fatal("a no-op edit made a backup")
	}
}

func TestEditRefusesSymlinks(t *testing.T) {
	ctx := context.Background()
	for _, which := range []string{"file", "dir"} {
		t.Run(which, func(t *testing.T) {
			r, home := shHome(t)
			real := filepath.Join(t.TempDir(), "real")
			if err := os.MkdirAll(real, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(real, "authorized_keys"), []byte(laptopKey+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if which == "dir" {
				if err := os.Symlink(real, filepath.Join(home, ".ssh")); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
				if err := os.Symlink(filepath.Join(real, "authorized_keys"), filepath.Join(home, ".ssh", "authorized_keys")); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := Edit(ctx, r, transport.Target{}, home, true, appendEdit(Grant{"gpu-box", id(gpuKey)}))
			var se *transport.SymlinkError
			if !errors.As(err, &se) || !se.Add || len(se.Lines) != 1 || !strings.Contains(err.Error(), "Make these 1 change(s)") {
				t.Fatalf("want a symlink refusal, got %v", err)
			}
			if got, _ := os.ReadFile(filepath.Join(real, "authorized_keys")); string(got) != laptopKey+"\n" {
				t.Fatalf("wrote through the link:\n%s", got)
			}
			// the write script refuses on its own too
			f, _ := Read(ctx, r, transport.Target{}, home)
			f.Link = ""
			if _, err := Write(ctx, r, transport.Target{}, home, f, "x\n"); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("write script: %v", err)
			}
		})
	}
}

func TestWriteRefusesChangedFile(t *testing.T) {
	r, home := shHome(t)
	ctx := context.Background()
	ak := filepath.Join(home, ".ssh", "authorized_keys")
	_ = os.MkdirAll(filepath.Dir(ak), 0o700)
	_ = os.WriteFile(ak, []byte(laptopKey+"\n"), 0o600)
	f, err := Read(ctx, r, transport.Target{}, home)
	if err != nil || f.Content != laptopKey+"\n" {
		t.Fatalf("read %q %v", f.Content, err)
	}
	_ = os.WriteFile(ak, []byte(deskKey+"\n"), 0o600)
	if _, err := Write(ctx, r, transport.Target{}, home, f, "x\n"); !errors.Is(err, ErrChanged) {
		t.Fatalf("want ErrChanged, got %v", err)
	}
	if got, _ := os.ReadFile(ak); string(got) != deskKey+"\n" {
		t.Fatalf("overwrote a newer file:\n%s", got)
	}
}
