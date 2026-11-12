package mesh

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/tailnet"
)

const (
	laptopKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlaptop me@laptop"
	gpuKey    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIgpu me@gpu-box"
	deskKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIdesk me@desk"
)

func id(k string) string { f := strings.Fields(k); return f[0] + " " + f[1] }

func TestAppendOnlyAndIdempotent(t *testing.T) {
	orig := "# mine\nrestrict ssh-rsa AAAAB3hand me@old\n" + `command="x" ` + id(deskKey) // no trailing newline
	gs := []Grant{{"laptop", id(laptopKey)}, {"desk", id(deskKey)}, {"gpu-box", id(gpuKey)}}
	got, lines := Append(orig, gs)
	if !strings.HasPrefix(got, orig+"\n") {
		t.Fatalf("existing lines changed:\n%s", got)
	}
	want := []string{id(laptopKey) + " yonder:laptop", id(gpuKey) + " yonder:gpu-box"}
	if !slices.Equal(lines, want) {
		t.Fatalf("added %q, want %q", lines, want)
	}
	if again, more := Append(got, gs); again != got || more != nil {
		t.Fatalf("second append changed it: %q", more)
	}
	for _, l := range lines {
		if TagOf(l) == "" {
			t.Fatalf("untagged %q", l)
		}
	}
}

func TestAppendSkipsCommentedOutKey(t *testing.T) {
	if _, lines := Append("# "+gpuKey+"\n", []Grant{{"gpu-box", id(gpuKey)}}); lines != nil {
		t.Fatalf("re-added a key the user commented out: %q", lines)
	}
}

func TestStripTag(t *testing.T) {
	content := laptopKey + "\n" + // by hand: never touched
		id(gpuKey) + " yonder:gpu-box\n" +
		id(deskKey) + " yonder:gpu-box\n" + // gpu-box was first; desk shares the key
		id(laptopKey) + " yonder:laptop\n"
	got, gone := StripTag(content, "gpu-box", map[string]bool{id(deskKey): true})
	want := laptopKey + "\n" + id(deskKey) + " yonder:gpu-box\n" + id(laptopKey) + " yonder:laptop\n"
	if got != want || len(gone) != 1 {
		t.Fatalf("got\n%s\ngone %q", got, gone)
	}
	if got, gone := StripTag(content, "pc", nil); got != content || gone != nil {
		t.Fatalf("stripped lines of another machine: %q", gone)
	}
}

func TestSources(t *testing.T) {
	st := &tailnet.Status{
		Self: tailnet.Peer{DNSName: "laptop.example.ts.net", TailscaleIPs: []string{"100.64.0.1"}, UserID: 7},
		Peers: []tailnet.Peer{
			{DNSName: "gpu-box.example.ts.net", TailscaleIPs: []string{"100.64.0.2"}, UserID: 7},
			{DNSName: "desk.example.ts.net", TailscaleIPs: []string{"100.64.0.3"}, UserID: 7},
			{DNSName: "moved.example.ts.net", TailscaleIPs: []string{"100.64.0.99"}, UserID: 7},
			{DNSName: "friend.example.ts.net", TailscaleIPs: []string{"100.64.0.5"}, UserID: 8},
			{DNSName: "gone.example.ts.net", TailscaleIPs: []string{"100.64.0.6"}, UserID: 7},
		},
	}
	rec := func(name, ip string, keys ...string) registry.Machine {
		return registry.Machine{Name: name, DNSName: name + ".example.ts.net", TailscaleIP: ip, SSHHostKey: "ssh-ed25519 AAAAhost", SSHClientKeys: keys}
	}
	lostKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIlost me@old"
	ms := []registry.Machine{
		rec("laptop", "100.64.0.1", laptopKey),
		rec("gpu-box", "100.64.0.2", gpuKey, lostKey, "ssh-ed25519 AAAA\ninjected", `command="sh" ssh-ed25519 AAAAopts`),
		rec("desk", "100.64.0.3"),
		rec("moved", "100.64.0.4", deskKey),
		rec("friend", "100.64.0.5", deskKey),
		rec("gone", "100.64.0.6", deskKey),
		{Name: "pend", DNSName: "pend.example.ts.net", Pending: true, SSHClientKeys: []string{deskKey}},
	}
	ts := []registry.Tombstone{{Name: "gone"}, {Name: "old", Lost: true, SSHClientKeys: []string{lostKey}}}
	src, skip := Sources("laptop", ms, ts, st, false)
	var names []string
	for _, s := range src {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"laptop", "gpu-box"}) {
		t.Fatalf("sources %v, skipped %+v", names, skip)
	}
	// the lost key and the one with a newline are never handed out; options are dropped
	if got := src[1].Keys; !slices.Equal(got, []string{id(gpuKey), "ssh-ed25519 AAAAopts"}) {
		t.Fatalf("gpu-box keys %q", got)
	}
	why := map[string]string{}
	for _, s := range skip {
		why[s.Name] = s.Why
	}
	for name, part := range map[string]string{"desk": "no ssh client keys", "moved": "address changed", "friend": "doesn't own", "gone": "removed", "pend": "unfinished"} {
		if !strings.Contains(why[name], part) {
			t.Errorf("%s: why %q, want %q", name, why[name], part)
		}
	}
	if src, _ := Sources("laptop", ms, ts, st, true); len(src) != 3 {
		t.Fatalf("--allow-shared should take friend: %+v", src)
	}
}

func TestGrantsFocusAndUnreached(t *testing.T) {
	src := []Source{{"laptop", []string{id(laptopKey)}}, {"gpu-box", []string{id(gpuKey)}}, {"desk", []string{id(deskKey)}}}
	if gs := Grants("desk", "", src); len(gs) != 2 {
		t.Fatalf("desk grants %+v", gs)
	}
	// focus on gpu-box: desk only learns gpu-box's key; gpu-box learns everyone's
	if gs := Grants("desk", "gpu-box", src); len(gs) != 1 || gs[0].Machine != "gpu-box" {
		t.Fatalf("focused desk grants %+v", gs)
	}
	if gs := Grants("gpu-box", "gpu-box", src); len(gs) != 2 {
		t.Fatalf("focused gpu-box grants %+v", gs)
	}
	if un := Unreached(laptopKey+"\n", "gpu-box", src); !slices.Equal(un, []string{"desk"}) {
		t.Fatalf("unreached %v", un)
	}
}

func TestLeftover(t *testing.T) {
	content := id(gpuKey) + " yonder:gpu-box\n" + id(deskKey) + " yonder:gpu-box\n"
	live := []registry.Machine{{Name: "desk", SSHClientKeys: []string{deskKey}}}
	ts := []registry.Tombstone{{Name: "gpu-box"}}
	if got := Leftover(content, ts, live); got["gpu-box"] != 1 {
		t.Fatalf("leftover %v", got)
	}
	ts[0].IncludeShared = true
	if got := Leftover(content, ts, live); got["gpu-box"] != 2 {
		t.Fatalf("include_shared leftover %v", got)
	}
}

// sync runs this on every machine, for machines that were offline at remove time
func TestStripRemovedOnSync(t *testing.T) {
	t.Setenv("YONDER_HOME", t.TempDir())
	ak := filepath.Join(registry.UserHome(), ".ssh", "authorized_keys")
	_ = os.MkdirAll(filepath.Dir(ak), 0o700)
	orig := gpuKey + "\n" + id(gpuKey) + " yonder:gpu-box\n" + id(deskKey) + " yonder:desk\n"
	if err := os.WriteFile(ak, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := []registry.Tombstone{{Name: "gpu-box"}}
	msgs, err := StripRemoved(ts, []registry.Machine{{Name: "desk", SSHClientKeys: []string{deskKey}}})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("%q %v", msgs, err)
	}
	if got, _ := os.ReadFile(ak); string(got) != gpuKey+"\n"+id(deskKey)+" yonder:desk\n" {
		t.Fatalf("after:\n%s", got)
	}
	bs, _ := filepath.Glob(ak + ".yonder-bak.*")
	if len(bs) != 1 {
		t.Fatalf("backups %v", bs)
	}
	if msgs, err := StripRemoved(ts, nil); err != nil || msgs != nil {
		t.Fatalf("second run: %q %v", msgs, err)
	}
	if bs2, _ := filepath.Glob(ak + ".yonder-bak.*"); len(bs2) != 1 {
		t.Fatal("a no-op strip made a backup")
	}
}
