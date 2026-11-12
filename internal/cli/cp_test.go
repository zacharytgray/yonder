package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/runlog"
	"github.com/zacharytgray/yonder/internal/tailnet"
	"github.com/zacharytgray/yonder/internal/transport"
)

// cpApp is laptop with a peer gpu-box. the fake ssh runs gpu-box's hidden
// commands in process, so its files are this test's temp dirs.
func cpApp(t *testing.T, online bool) (*app, *fakeSSH, *strings.Builder) {
	t.Helper()
	fake := &fakeSSH{yonder: func(_ transport.Target, args []string, stdio transport.IO) (int, error) {
		return remote.Dispatch(t.Context(), args, stdio.Stdin, stdio.Stdout, stdio.Stderr), nil
	}}
	a, _ := testApp(t, fake, &tailnet.Status{Self: peer("laptop", true), Peers: []tailnet.Peer{peer("gpu-box", online)}})
	var out strings.Builder
	a.out, a.errw = &out, &out
	a.local = transport.Local{Handler: remote.Dispatch}
	if err := a.store.SetSelf("laptop"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"laptop", "gpu-box"} {
		m := registry.Machine{Name: n, TailscaleIP: "100.64.0.9", DNSName: n + ".example.ts.net", SSHHostKey: "ssh-ed25519 AAAAhost", UpdatedAt: time.Now().UTC()}
		if err := a.store.SaveMachine(m); err != nil {
			t.Fatal(err)
		}
	}
	return a, fake, &out
}

func mkfile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keynote(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "Talk.key")
	mkfile(t, filepath.Join(src, "Index.apxl"), "slides")
	mkfile(t, filepath.Join(src, "Data", "img.bin"), strings.Repeat("x", 2048))
	return src
}

func TestCpLocalToRemote(t *testing.T) {
	a, fake, out := cpApp(t, true)
	src := keynote(t)
	dest := t.TempDir()
	if err := runCopy(a, []string{src}, "gpu-box:"+dest+"/", false, "claude-code"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "copied Talk.key (folder, 2 files, 2.0 KB) to gpu-box:" + filepath.Join(dest, "Talk.key") + "\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "Talk.key", "Data", "img.bin")); len(b) != 2048 {
		t.Fatal("package contents missing")
	}
	if strings.Join(fake.yonders, ",") != "gpu-box _recv" {
		t.Fatalf("ssh calls %v", fake.yonders)
	}

	// the receiver logged it: start, then completion with bytes
	es, _ := runlog.Read(registry.LogPath(), time.Time{})
	if len(es) != 2 || es[1].Files != 2 || es[1].Bytes != 2054 || !es[1].Copy || es[1].From != "laptop" || es[1].To != "gpu-box" || es[1].Harness != "claude-code" {
		t.Fatalf("log %+v", es)
	}
	if !strings.HasPrefix(es[1].Command, "cp laptop:"+src+" -> gpu-box:"+dest+"/") {
		t.Fatalf("command %q", es[1].Command)
	}

	// again: refused without --force, kept as it was
	out.Reset()
	err := runCopy(a, []string{src}, "gpu-box:"+dest+"/", false, "shell")
	if ExitCode(err) != 1 || !strings.Contains(out.String(), "already exists; add --force") {
		t.Fatalf("err %v out %s", err, out)
	}
	out.Reset()
	if err := runCopy(a, []string{src}, "gpu-box:"+dest+"/", true, "shell"); err != nil {
		t.Fatalf("--force: %v %s", err, out)
	}
}

func TestCpRemoteToLocal(t *testing.T) {
	a, fake, out := cpApp(t, true)
	src := keynote(t)
	dest := t.TempDir()
	if err := runCopy(a, []string{"gpu-box:" + src}, dest, false, "shell"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.HasPrefix(out.String(), "copied Talk.key (folder, 2 files") {
		t.Fatalf("out %q", out)
	}
	if _, err := os.Stat(filepath.Join(dest, "Talk.key", "Index.apxl")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fake.yonders, ",") != "gpu-box _send" {
		t.Fatalf("ssh calls %v", fake.yonders)
	}

	// a missing source: the sender's reason, nothing copied
	out.Reset()
	err := runCopy(a, []string{"gpu-box:" + filepath.Join(src, "nope")}, dest+"/", false, "shell")
	if ExitCode(err) != 1 || !strings.Contains(out.String(), "no such file or folder") {
		t.Fatalf("err %v out %s", err, out)
	}
}

func TestCpOffline(t *testing.T) {
	a, fake, _ := cpApp(t, false)
	err := runCopy(a, []string{keynote(t)}, "gpu-box:~/Desktop/", false, "shell")
	if ExitCode(err) != 75 || !strings.Contains(err.Error(), "gpu-box is offline") {
		t.Fatalf("err %v", err)
	}
	if len(fake.yonders) != 0 {
		t.Fatalf("ssh calls %v", fake.yonders)
	}
	es, _ := runlog.Read(registry.LogPath(), time.Time{})
	if len(es) != 1 || es[0].Side != runlog.SideCaller || es[0].To != "gpu-box" || *es[0].Exit != 75 {
		t.Fatalf("log %+v", es)
	}
}

func TestCpUnreachableSender(t *testing.T) {
	a, fake, _ := cpApp(t, true)
	fake.yonder = func(transport.Target, []string, transport.IO) (int, error) {
		return 255, &transport.UnreachableError{Target: "gpu-box", Detail: "ssh: connect to host 100.64.0.9 port 22: Connection refused"}
	}
	err := runCopy(a, []string{"gpu-box:~/x"}, t.TempDir()+"/", false, "shell")
	if ExitCode(err) != 75 || !strings.Contains(err.Error(), "unreachable over ssh") {
		t.Fatalf("err %v", err)
	}
}

func TestCpUsage(t *testing.T) {
	a, _, _ := cpApp(t, true)
	for _, c := range [][]string{
		{"a.txt", "b.txt"},              // no machine anywhere
		{"gpu-box:a", "laptop:b", "./"}, // sources on two machines
		{"gpu-box:", "./"},              // no source path
	} {
		err := runCopy(a, c[:len(c)-1], c[len(c)-1], false, "shell")
		if ExitCode(err) != 2 {
			t.Errorf("%v: err %v", c, err)
		}
	}
	if err := runCopy(a, []string{"nope:x"}, "./", false, "shell"); err == nil || !strings.Contains(err.Error(), "no machine named nope") {
		t.Fatalf("err %v", err)
	}
}

func TestSplitSide(t *testing.T) {
	cases := map[string][2]string{
		"gpu-box:~/x":   {"gpu-box", "~/x"},
		"gpu-box:":      {"gpu-box", ""},
		"./a:b":         {"", "./a:b"},
		"/tmp/a:b":      {"", "/tmp/a:b"},
		"plain.txt":     {"", "plain.txt"},
		":odd":          {"", ":odd"},
		"desk.local:/x": {"desk.local", "/x"},
	}
	for in, want := range cases {
		if m, p := splitSide(in); m != want[0] || p != want[1] {
			t.Errorf("splitSide(%q) = %q, %q", in, m, p)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KB", 5 << 20: "5.0 MB", 3 << 30: "3.0 GB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
