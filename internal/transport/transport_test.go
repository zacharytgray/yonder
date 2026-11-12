package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"plain":           "plain",
		"/a/b-c_d.e":      "/a/b-c_d.e",
		"":                "''",
		"a b":             "'a b'",
		"it's":            `'it'\''s'`,
		"$HOME; rm -rf /": `'$HOME; rm -rf /'`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
	// what the shell sees must be the original args
	args := []string{"printf", "%s|", "a b", "it's", "$x", ""}
	out, err := exec.Command("/bin/sh", "-c", Join(args)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a b|it's|$x||" {
		t.Fatalf("got %q", out)
	}
}

// fakeSSH writes a script that stands in for ssh. it can see its own args.
func fakeSSH(t *testing.T, body string) *SSH {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &SSH{Binary: path}
}

var tgt = Target{Name: "gpu-box", Host: "100.64.0.2", Port: 22, User: "me", HostKey: "ssh-ed25519 AAAAexample", YonderPath: "/home/me/.local/bin/yonder"}

func TestExitCodes(t *testing.T) {
	ctx := context.Background()
	s := fakeSSH(t, `echo "ssh: connect to host 100.64.0.2 port 22: Connection refused" >&2; exit 255`)
	code, err := s.Yonder(ctx, tgt, []string{"_exec"}, IO{Stderr: io.Discard})
	if !errors.Is(err, ErrUnreachable) || code != 255 {
		t.Fatalf("255 should be unreachable: %d %v", code, err)
	}
	ue, _ := IsUnreachable(err)
	if !strings.Contains(ue.Detail, "Connection refused") || ue.AuthFailed() {
		t.Fatalf("detail %q", ue.Detail)
	}

	s = fakeSSH(t, `exit 42`)
	if code, err := s.Yonder(ctx, tgt, []string{"_exec"}, IO{}); code != 42 || err != nil {
		t.Fatalf("remote code should pass through: %d %v", code, err)
	}

	s = fakeSSH(t, `echo "me@100.64.0.2: Permission denied (publickey)." >&2; exit 255`)
	_, err = s.Shell(ctx, tgt, "true", IO{Stderr: io.Discard})
	if ue, ok := IsUnreachable(err); !ok || !ue.AuthFailed() {
		t.Fatalf("auth failure: %v", err)
	}

	if _, err := s.Shell(ctx, Target{Name: "x"}, "true", IO{}); err == nil || errors.Is(err, ErrUnreachable) {
		t.Fatal("no pinned key should fail before ssh runs")
	}
}

func TestPinnedArgs(t *testing.T) {
	// echo back the args and the known_hosts content ssh was handed
	s := fakeSSH(t, `for a in "$@"; do echo "$a"; done
for a in "$@"; do case "$a" in UserKnownHostsFile=*) cat "${a#UserKnownHostsFile=}";; esac; done`)
	var out bytes.Buffer
	if _, err := s.Yonder(context.Background(), tgt, []string{"_exec"}, IO{Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"BatchMode=yes", "ConnectTimeout=5", "Port=22", "User=me", "StrictHostKeyChecking=yes",
		"HostKeyAlias=yonder-gpu-box", "yonder-gpu-box ssh-ed25519 AAAAexample",
		"/home/me/.local/bin/yonder _exec",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPinReadsKey(t *testing.T) {
	// pretend to be ssh with accept-new: record the key, then fail auth
	s := fakeSSH(t, `for a in "$@"; do case "$a" in UserKnownHostsFile=*) kh="${a#UserKnownHostsFile=}";; esac; done
echo "yonder-gpu-box ssh-ed25519 AAAAnewkey" >> "$kh"
echo "Permission denied (publickey)." >&2
exit 255`)
	key, _, err := s.Pin(context.Background(), Target{Name: "gpu-box", Host: "100.64.0.2", User: "me"}, "true", IO{Stderr: io.Discard})
	if key != "ssh-ed25519 AAAAnewkey" {
		t.Fatalf("key %q", key)
	}
	if ue, ok := IsUnreachable(err); !ok || !ue.AuthFailed() {
		t.Fatalf("err %v", err)
	}
}

func TestLocalShell(t *testing.T) {
	var out bytes.Buffer
	code, err := Local{}.Shell(context.Background(), Target{}, "cat; exit 5", IO{Stdin: strings.NewReader("in"), Stdout: &out})
	if err != nil || code != 5 || out.String() != "in" {
		t.Fatalf("%d %v %q", code, err, out.String())
	}
}

func TestQuoteLeadingEqualsInEveryShell(t *testing.T) {
	if Quote("=ls") != "'=ls'" || Quote("FOO=bar") != "FOO=bar" {
		t.Fatalf("Quote: %s %s", Quote("=ls"), Quote("FOO=bar"))
	}
	home := t.TempDir() // no rc files from the real home
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("BASH_ENV", "")
	t.Setenv("ENV", "")
	tried := 0
	for _, sh := range []string{"sh", "bash", "zsh"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		tried++
		// zsh expands an unquoted =ls to the path of ls
		out, err := exec.Command(path, "-c", Join([]string{"printf", "%s|", "=ls", "a=b"})).Output()
		if err != nil || string(out) != "=ls|a=b|" {
			t.Errorf("%s: %q %v", sh, out, err)
		}
		// an env assignment still works as one
		out, err = exec.Command(path, "-c", Join([]string{"FOO=bar", "sh", "-c", "echo $FOO"})).Output()
		if err != nil || string(out) != "bar\n" {
			t.Errorf("%s env assignment: %q %v", sh, out, err)
		}
	}
	if tried == 0 {
		t.Skip("no shell found")
	}
}

func TestIdentityOnlyForItsTarget(t *testing.T) {
	// ssh and scp both just print their args
	echo := `for a in "$@"; do echo "$a"; done`
	s := fakeSSH(t, echo)
	run := func(t Target) string {
		var out bytes.Buffer
		if _, err := s.Yonder(context.Background(), t, []string{"_exec"}, IO{Stdout: &out}); err != nil {
			panic(err)
		}
		return out.String()
	}
	with := tgt
	with.Identity = "/keys/id_example"
	got := run(with)
	for _, want := range []string{"-i\n/keys/id_example\n", "IdentitiesOnly=yes"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if got := run(tgt); strings.Contains(got, "-i\n") || strings.Contains(got, "IdentitiesOnly") {
		t.Errorf("target without a key got one:\n%s", got)
	}

	// the per-target key beats the runner-wide one (YONDER_SSH_IDENTITY)
	s.Identity = "/keys/global"
	if got := run(with); !strings.Contains(got, "/keys/id_example") || strings.Contains(got, "/keys/global") {
		t.Errorf("per-target should win:\n%s", got)
	}
	if got := run(tgt); !strings.Contains(got, "/keys/global") {
		t.Errorf("runner-wide key is the fallback:\n%s", got)
	}
}

func TestIdentityOnScpAndCopyID(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"scp", "ssh-copy-id"} {
		body := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > " + filepath.Join(dir, bin+".args") + "\n"
		if err := os.WriteFile(filepath.Join(dir, bin), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := &SSH{}
	with := tgt
	with.Identity = "/keys/id_example"
	if err := s.Copy(context.Background(), with, "/tmp/f", "f"); err != nil {
		t.Fatal(err)
	}
	args := func(bin string) string {
		b, _ := os.ReadFile(filepath.Join(dir, bin+".args"))
		return string(b)
	}
	if got := args("scp"); !strings.Contains(got, "-i\n/keys/id_example\n") || !strings.Contains(got, "IdentitiesOnly=yes") {
		t.Errorf("scp args:\n%s", got)
	}
	if err := s.CopyID(context.Background(), with, IO{}); err != nil {
		t.Fatal(err)
	}
	if got := args("ssh-copy-id"); !strings.Contains(got, "-i\n/keys/id_example\n") || !strings.Contains(got, "IdentitiesOnly=yes") {
		t.Errorf("ssh-copy-id args:\n%s", got)
	}
	if err := s.CopyID(context.Background(), tgt, IO{}); err != nil {
		t.Fatal(err)
	}
	if got := args("ssh-copy-id"); strings.Contains(got, "-i\n") {
		t.Errorf("no key set, but:\n%s", got)
	}
}
