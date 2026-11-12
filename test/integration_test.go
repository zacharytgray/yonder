//go:build integration

package test

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// a throwaway sshd per fake machine. host keys are made at start so each differs.
const dockerfile = `FROM alpine:3.20
RUN apk add --no-cache openssh-server \
 && adduser -D -s /bin/sh me \
 && echo "me:$(head -c 18 /dev/urandom | base64)" | chpasswd \
 && mkdir -p /home/me/.ssh && chmod 700 /home/me/.ssh && chown me:me /home/me/.ssh \
 && sed -i 's/^#*PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && echo 'PermitUserEnvironment yes' >> /etc/ssh/sshd_config
CMD ["sh", "-c", "ssh-keygen -A >/dev/null && exec /usr/sbin/sshd -D -e"]
`

const image = "yonder-it-sshd:latest"

func docker(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("docker", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type node struct {
	name, container string
	port            int
}

func startNode(t *testing.T, name, pubKey, linuxBin string) node {
	t.Helper()
	var b [3]byte
	_, _ = rand.Read(b[:])
	c := "yonder-it-" + name + "-" + hex.EncodeToString(b[:])
	docker(t, "", "run", "-d", "--rm", "--name", c, "-p", "127.0.0.1::22", image)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", c).Run() })
	docker(t, pubKey, "exec", "-i", c, "sh", "-c",
		"cat > /home/me/.ssh/authorized_keys && chown me:me /home/me/.ssh/authorized_keys && chmod 600 /home/me/.ssh/authorized_keys")
	if linuxBin != "" {
		// host can't hand over its own binary, so preinstall a linux build
		docker(t, "", "cp", linuxBin, c+":/usr/local/bin/yonder")
		docker(t, "", "exec", c, "chmod", "755", "/usr/local/bin/yonder")
	}
	hostPort := docker(t, "", "port", c, "22/tcp")
	var port int
	if _, err := fmt.Sscanf(hostPort[strings.LastIndex(hostPort, ":")+1:], "%d", &port); err != nil {
		t.Fatalf("port %q: %v", hostPort, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if out, err := exec.Command("ssh-keyscan", "-p", fmt.Sprint(port), "127.0.0.1").Output(); err == nil && len(out) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sshd in %s never came up", c)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return node{name: name, container: c, port: port}
}

func writeFixture(t *testing.T, names ...string) string {
	t.Helper()
	peers := map[string]any{}
	for i, n := range names {
		peers[fmt.Sprintf("nodekey:%d", i)] = map[string]any{
			"HostName": n, "DNSName": n + ".example.ts.net.", "OS": "linux",
			"TailscaleIPs": []string{"127.0.0.1"}, "Online": true, "LastSeen": "0001-01-01T00:00:00Z", "UserID": 1001,
		}
	}
	st := map[string]any{
		"BackendState": "Running",
		"Self": map[string]any{
			"HostName": "laptop", "DNSName": "laptop.example.ts.net.", "OS": runtime.GOOS,
			"TailscaleIPs": []string{"127.0.0.1"}, "Online": true, "UserID": 1001,
		},
		"Peer": peers,
	}
	data, _ := json.Marshal(st)
	path := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// containerFixture gives a node a tailscale status where "laptop" has src (what the
// node sees as the caller) and 127.0.0.1 (laptop's record), via ~/.ssh/environment.
func containerFixture(t *testing.T, n node, src string) {
	t.Helper()
	st := map[string]any{
		"BackendState": "Running",
		"Self":         map[string]any{"HostName": n.name, "DNSName": n.name + ".example.ts.net.", "TailscaleIPs": []string{"127.0.0.1"}, "UserID": 1001},
		"Peer": map[string]any{"nodekey:laptop": map[string]any{
			"HostName": "laptop", "DNSName": "laptop.example.ts.net.", "OS": runtime.GOOS,
			"TailscaleIPs": []string{src, "127.0.0.1"}, "Online": true, "UserID": 1001,
		}},
	}
	data, _ := json.Marshal(st)
	docker(t, string(data), "exec", "-i", n.container, "sh", "-c",
		"cat > /home/me/status.json && echo YONDER_TAILSCALE_JSON=/home/me/status.json > /home/me/.ssh/environment"+
			" && chown me:me /home/me/status.json /home/me/.ssh/environment && chmod 600 /home/me/.ssh/environment")
}

// TestFleetOverSSH runs two sshd containers as a fake fleet.
func TestFleetOverSSH(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker not running")
	}
	docker(t, dockerfile, "build", "-q", "-t", image, "-")

	keyDir := t.TempDir()
	key := filepath.Join(keyDir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")

	// on a linux host of the same arch, add copies the running binary over;
	// anywhere else, preinstall a linux build in the containers
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[docker(t, "", "info", "--format", "{{.Architecture}}")]
	linuxBin := ""
	if runtime.GOOS != "linux" || runtime.GOARCH != arch {
		linuxBin = filepath.Join(t.TempDir(), "yonder")
		if err := build(linuxBin, "linux", arch); err != nil {
			t.Fatal(err)
		}
	}

	a := startNode(t, "node-a", string(pub), linuxBin)
	b := startNode(t, "node-b", string(pub), linuxBin)

	f := newFleet(t, writeFixture(t, "node-a", "node-b", "node-c"))
	f.env = append(f.env, "YONDER_SSH_IDENTITY="+key)
	f.must("init", "--yes")

	for i, n := range []node{a, b} {
		r := f.must("add", n.name, "--user", "me", "--port", fmt.Sprint(n.port), "--yes", "--notes", "container")
		if !strings.Contains(r.stdout, "added "+n.name+" (linux/") {
			t.Fatalf("add %s: %+v", n.name, r)
		}
		if i == 0 {
			// _sync names its caller from SSH_CONNECTION and tailscale status, so each
			// container gets a status fixture listing this host at the address it sees
			src := strings.Fields(f.must("run", a.name, "--", "echo $SSH_CONNECTION").stdout)[0]
			for _, c := range []node{a, b} {
				containerFixture(t, c, src)
			}
		}
	}
	if r := f.must("refresh"); strings.Contains(r.stderr, "sync with") {
		t.Fatalf("a fleet machine's sync was refused: %s", r.stderr)
	}

	if r := f.must("run", "node-a", "--", "echo hi from $(whoami)"); r.stdout != "hi from me\n" {
		t.Fatalf("run: %+v", r)
	}
	if r := f.run("", "run", "node-a", "--", "exit 7"); r.code != 7 {
		t.Fatalf("exit passthrough: %+v", r)
	}
	if r := f.run("over ssh\n", "run", "node-b", "--", "cat"); r.stdout != "over ssh\n" {
		t.Fatalf("stdin passthrough: %+v", r)
	}

	// the registry reached node-b, and node-b knows its own name
	r := f.must("run", "node-b", "--", "cat ~/.config/yonder/machines/node-a.toml ~/.config/yonder/config.toml")
	if !strings.Contains(r.stdout, "node-a") || !strings.Contains(r.stdout, "node-b") || !strings.Contains(r.stdout, "ssh_host_key") {
		t.Fatalf("registry on node-b: %s", r.stdout)
	}

	var ls struct {
		Machines []struct{ Name, State string } `json:"machines"`
	}
	if err := json.Unmarshal([]byte(f.must("ls", "--json").stdout), &ls); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, m := range ls.Machines {
		states[m.Name] = m.State
	}
	if len(states) != 3 || states["laptop"] != "self" || states["node-a"] != "online" || states["node-b"] != "online" {
		t.Fatalf("ls: %+v", ls)
	}

	// detached job on a remote machine
	id := strings.TrimSpace(f.must("run", "--detach", "node-a", "--", "sleep 1; echo bg done").stdout)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if r := f.run("", "jobs", "node-a", id); r.stdout == "bg done\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never finished", id)
		}
		time.Sleep(300 * time.Millisecond)
	}

	// log merges node-a's own entries
	r = f.must("log", "--json", "--machine", "node-a", "--limit", "0")
	if !strings.Contains(r.stdout, `"side": "target"`) || !strings.Contains(r.stdout, "whoami") {
		t.Fatalf("log: %s", r.stdout)
	}

	// cp: a package folder to node-a, relayed from node-a to node-b, then back here
	pkg := filepath.Join(t.TempDir(), "Talk.key")
	if err := os.MkdirAll(filepath.Join(pkg, "Data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "Data", "img.bin"), []byte(strings.Repeat("x", 70000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Data/img.bin", filepath.Join(pkg, "preview")); err != nil {
		t.Fatal(err)
	}
	if r := f.must("cp", pkg, "node-a:~/Desktop/"); !strings.HasPrefix(r.stdout, "copied Talk.key (folder, 1 file, 68.4 KB) to node-a:/home/me/Desktop/Talk.key") {
		t.Fatalf("cp to node-a: %+v", r)
	}
	if r := f.run("", "cp", pkg, "node-a:~/Desktop/"); r.code != 1 || !strings.Contains(r.stderr, "already exists; add --force") {
		t.Fatalf("no-overwrite: %+v", r)
	}
	f.must("cp", "node-a:Desktop/Talk.key", "node-b:/tmp/")
	back := t.TempDir()
	f.must("cp", "node-b:/tmp/Talk.key", back)
	if b, _ := os.ReadFile(filepath.Join(back, "Talk.key", "Data", "img.bin")); len(b) != 70000 {
		t.Fatalf("round trip lost data: %d bytes", len(b))
	}
	if l, _ := os.Readlink(filepath.Join(back, "Talk.key", "preview")); l != "Data/img.bin" {
		t.Fatalf("link: %q", l)
	}
	if r := f.must("log", "--json", "--machine", "node-b", "--limit", "0"); !strings.Contains(r.stdout, `"copy": true`) || !strings.Contains(r.stdout, `"bytes": 70000`) {
		t.Fatalf("node-b should log the copy it received: %s", r.stdout)
	}

	if r := f.must("refresh"); !strings.Contains(r.stdout, "refreshed node-a") || !strings.Contains(r.stdout, "refreshed node-b") {
		t.Fatalf("refresh: %+v", r)
	}

	// node-c: a record pointing at node-a's port with node-b's host key must be refused (pinning)
	machines := filepath.Join(f.home, "yonder", "config", "machines")
	recA, _ := os.ReadFile(filepath.Join(machines, "node-a.toml"))
	recB, _ := os.ReadFile(filepath.Join(machines, "node-b.toml"))
	keyB := ""
	for _, line := range strings.Split(string(recB), "\n") {
		if strings.HasPrefix(line, "ssh_host_key") {
			keyB = line
		}
	}
	var recC []string
	for _, line := range strings.Split(string(recA), "\n") {
		switch {
		case strings.HasPrefix(line, "name"):
			line = "name = 'node-c'"
		case strings.HasPrefix(line, "ssh_host_key"):
			line = keyB
		}
		recC = append(recC, line)
	}
	if err := os.WriteFile(filepath.Join(machines, "node-c.toml"), []byte(strings.Join(recC, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := f.run("", "run", "node-c", "--", "true"); r.code != 75 || !strings.Contains(r.stderr, "unreachable over ssh") {
		t.Fatalf("host key mismatch should be unreachable: %+v", r)
	}

	// a dead port is unreachable too; drop the crafted record first, it names node-a's dns
	if err := os.Remove(filepath.Join(machines, "node-c.toml")); err != nil {
		t.Fatal(err)
	}
	if r := f.run("", "add", "node-c", "--user", "me", "--port", "1", "--yes"); r.code != 75 {
		t.Fatalf("dead port: %+v", r)
	}
}
