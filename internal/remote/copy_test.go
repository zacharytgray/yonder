package remote

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/runlog"
)

func write(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// a keynote-style package: a folder with nested files and links
func pkg(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "Talk.key")
	write(t, filepath.Join(src, "Index.apxl"), "slides")
	write(t, filepath.Join(src, "Data", "img.bin"), strings.Repeat("x", 3000))
	if err := os.Symlink("Data/img.bin", filepath.Join(src, "preview")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "outside")); err != nil {
		t.Fatal(err)
	}
	return src
}

func sendTar(t *testing.T, paths ...string) []byte {
	t.Helper()
	srcs, err := sendSources(paths)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeTar(context.Background(), srcs, &buf, io.Discard); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCopyRoundTrip(t *testing.T) {
	setup(t)
	src := pkg(t)
	dest := t.TempDir()
	var errb bytes.Buffer
	res, code, err := receive(context.Background(), CopySpec{Dest: dest + "/"}, bytes.NewReader(sendTar(t, src)), &errb)
	if err != nil || code != 0 {
		t.Fatalf("code %d err %v", code, err)
	}
	if res.Files != 2 || res.Bytes != 3006 || len(res.Items) != 1 || !res.Items[0].Dir || res.Items[0].Path != filepath.Join(dest, "Talk.key") {
		t.Fatalf("result %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "Talk.key", "Data", "img.bin")); len(b) != 3000 {
		t.Fatalf("img.bin %d bytes", len(b))
	}
	if l, err := os.Readlink(filepath.Join(dest, "Talk.key", "preview")); err != nil || l != "Data/img.bin" {
		t.Fatalf("inside link %q %v", l, err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "Talk.key", "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a link pointing outside the folder must not be made")
	}
	if !strings.Contains(errb.String(), "skipped link Talk.key/outside") {
		t.Fatalf("warning: %q", errb.String())
	}
	// no staging folder left behind
	if ents, _ := os.ReadDir(dest); len(ents) != 1 {
		t.Fatalf("dest holds %v", ents)
	}
}

func TestCopyRenameAndOverwrite(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.txt")
	write(t, src, "new")
	dest := filepath.Join(t.TempDir(), "renamed.txt")
	if _, code, err := receive(context.Background(), CopySpec{Dest: dest}, bytes.NewReader(sendTar(t, src)), io.Discard); code != 0 {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	write(t, dest, "old")
	_, code, err := receive(context.Background(), CopySpec{Dest: dest}, bytes.NewReader(sendTar(t, src)), io.Discard)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "already exists; add --force") {
		t.Fatalf("code %d err %v", code, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "old" {
		t.Fatal("an existing file was overwritten without --force")
	}
	if _, code, err := receive(context.Background(), CopySpec{Dest: dest, Force: true}, bytes.NewReader(sendTar(t, src)), io.Discard); code != 0 {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "new" {
		t.Fatalf("--force: got %q", b)
	}

	// several sources need a folder
	other := filepath.Join(dir, "b.txt")
	write(t, other, "b")
	if _, code, _ := receive(context.Background(), CopySpec{Dest: filepath.Join(t.TempDir(), "x")}, bytes.NewReader(sendTar(t, src, other)), io.Discard); code != 1 {
		t.Fatal("two items into a file name should fail")
	}
}

type tarEntry struct {
	name, link string
	typ        byte
	body       string
}

func craft(t *testing.T, marker bool, es ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var tot copyTotals
	for _, e := range es {
		h := &tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body))}
		if e.typ == tar.TypeDir {
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.body != "" {
			_, _ = tw.Write([]byte(e.body))
		}
		if e.typ == tar.TypeReg {
			tot.Files++
			tot.Bytes += int64(len(e.body))
		}
	}
	if marker {
		data, _ := json.Marshal(tot)
		_ = tw.WriteHeader(&tar.Header{Name: doneMarker, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(data))})
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	return buf.Bytes()
}

func TestCopyRefusesTarSlip(t *testing.T) {
	setup(t)
	cases := map[string][]tarEntry{
		"dotdot":   {{name: "../evil", typ: tar.TypeReg, body: "x"}},
		"absolute": {{name: "/tmp/evil", typ: tar.TypeReg, body: "x"}},
		"nested":   {{name: "a/", typ: tar.TypeDir}, {name: "a/../../evil", typ: tar.TypeReg, body: "x"}},
		// a link inside the folder, then a write through it
		"through link": {{name: "a/", typ: tar.TypeDir}, {name: "a/l", link: ".", typ: tar.TypeSymlink}, {name: "a/l/evil", typ: tar.TypeReg, body: "x"}},
		"orphan":       {{name: "a/evil", typ: tar.TypeReg, body: "x"}},
	}
	for name, es := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "in")
			_, code, err := receive(context.Background(), CopySpec{Dest: dest + "/"}, bytes.NewReader(craft(t, true, es...)), io.Discard)
			if code != 1 || err == nil || !strings.Contains(err.Error(), "refused") {
				t.Fatalf("code %d err %v", code, err)
			}
			ents, _ := os.ReadDir(dest)
			pents, _ := os.ReadDir(parent)
			if len(ents) != 0 || len(pents) != 1 {
				t.Fatalf("left behind: %v %v", ents, pents)
			}
		})
	}
}

func TestCopyNeedsEndMarker(t *testing.T) {
	setup(t)
	dest := t.TempDir()
	data := craft(t, false, tarEntry{name: "a.txt", typ: tar.TypeReg, body: "x"})
	_, code, err := receive(context.Background(), CopySpec{Dest: dest + "/"}, bytes.NewReader(data), io.Discard)
	if code != ExitCutOff || !errors.Is(err, errCutOff) {
		t.Fatalf("code %d err %v", code, err)
	}
	if ents, _ := os.ReadDir(dest); len(ents) != 0 {
		t.Fatalf("a cut-off copy left %v", ents)
	}
}

func TestLinkInside(t *testing.T) {
	cases := []struct {
		name, target string
		want         bool
	}{
		{"a/l", "b", true},
		{"a/b/l", "../c", true},
		{"a/b/l", "./c/d", true},
		{"a/l", "..", false},
		{"a/b/l", "../../x", false},
		{"a/b/l", "d/../..", false}, // d may itself be a link
		{"a/l", "/etc/passwd", false},
		{"a", "a/x", false},
		{"a/l", "", false},
	}
	for _, c := range cases {
		if got := linkInside(c.name, c.target); got != c.want {
			t.Errorf("linkInside(%q, %q) = %v", c.name, c.target, got)
		}
	}
}

// _recv logs a start entry and a completion with bytes, like _exec
func TestRecvLogs(t *testing.T) {
	setup(t)
	src := filepath.Join(t.TempDir(), "f.txt")
	write(t, src, "hello")
	dest := t.TempDir()
	env := Envelope{ID: "20261111-200000-ab12cd", Command: "cp laptop:" + src + " -> gpu-box:" + dest, From: "laptop", To: "gpu-box", Harness: "codex",
		Copy: &CopySpec{Dest: dest}}
	var out, errb bytes.Buffer
	if code := Recv(context.Background(), nil, input(t, env, string(sendTar(t, src))), &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	var res CopyResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Files != 1 {
		t.Fatalf("result %q %v", out.String(), err)
	}
	es, _ := runlog.Read(registry.LogPath(), time.Time{})
	if len(es) != 2 || es[0].Status != runlog.StatusStarted || !es[0].Copy {
		t.Fatalf("log %+v", es)
	}
	e := es[1]
	if !e.Copy || e.Files != 1 || e.Bytes != 5 || e.Exit == nil || *e.Exit != 0 || e.From != "laptop" || e.To != "gpu-box" || e.Harness != "codex" || e.Side != runlog.SideTarget {
		t.Fatalf("completion %+v", e)
	}

	// an _exec envelope with a copy in it is refused, never run as a shell command
	if code := Exec(context.Background(), nil, input(t, env, ""), io.Discard, io.Discard); code != 1 {
		t.Fatal("_exec ran a copy envelope")
	}
}
