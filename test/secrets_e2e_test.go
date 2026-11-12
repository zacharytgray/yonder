package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOp stands in for the 1password cli: inject swaps every reference for a dummy value.
const fakeOp = `#!/bin/sh
case "$1" in
--version) echo 2.34.0 ;;
whoami) exit 0 ;;
inject) sed -E 's/[{][{] op:[/][/][^}]* [}][}]/dummy-value-123/g' ;;
*) echo "[ERROR] 2026/11/11 20:00:00 unsupported" >&2; exit 1 ;;
esac
`

func TestSecretsEndToEnd(t *testing.T) {
	f := newFleet(t, "testdata/fleet.json")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "op"), []byte(fakeOp), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, kv := range f.env {
		if strings.HasPrefix(kv, "PATH=") {
			f.env[i] = "PATH=" + bin + ":" + strings.TrimPrefix(kv, "PATH=")
		}
	}
	f.must("init", "--yes")

	if r := f.run("", "secret", "add", "api_key", "op://example-vault/api/credential"); r.code != 2 {
		t.Fatalf("bad name should be a usage error: %+v", r)
	}
	if r := f.run("", "secret", "add", "API_KEY", "vault/item"); r.code != 2 {
		t.Fatalf("bad ref should be a usage error: %+v", r)
	}
	f.must("secret", "setup", "--provider", "1password", "--mode", "desktop")
	f.must("secret", "add", "API_KEY", "op://example-vault/api/credential")
	f.must("secret", "add", "OTHER", "op://example-vault/other/password")

	if r := f.must("secret", "ls"); !strings.Contains(r.stdout, "1password (desktop)") || !strings.Contains(r.stdout, "API_KEY  op://example-vault/api/credential") {
		t.Fatalf("ls: %s", r.stdout)
	}
	if r := f.must("secret", "ls", "--json"); !strings.Contains(r.stdout, `"name": "OTHER"`) || !strings.Contains(r.stdout, `"provider": "1password"`) {
		t.Fatalf("ls --json: %s", r.stdout)
	}
	r := f.must("secret", "test", "API_KEY")
	if r.stdout != "API_KEY: ok, 15 chars\n" || strings.Contains(r.stdout+r.stderr, "dummy-value") {
		t.Fatalf("test: %+v", r)
	}
	r = f.must("run", "laptop", "--secret", "API_KEY", "--", "echo $API_KEY; echo ${#API_KEY}")
	if r.stdout != "[API_KEY]\n15\n" {
		t.Fatalf("run --secret: %+v", r)
	}
	if r := f.run("", "run", "laptop", "--secret", "NOPE", "--", "true"); r.code != 1 || !strings.Contains(r.stderr, "known: API_KEY, OTHER") {
		t.Fatalf("unknown secret: %+v", r)
	}
	if r := f.run("tok\n", "secret", "token", "set"); r.code != 1 || !strings.Contains(r.stderr, "does not use a stored token") {
		t.Fatalf("token set in desktop mode: %+v", r)
	}
	f.must("secret", "rm", "OTHER")

	log, err := os.ReadFile(filepath.Join(f.home, "yonder", "state", "log.jsonl"))
	if err != nil || !strings.Contains(string(log), `"secrets":["API_KEY"]`) || strings.Contains(string(log), "dummy-value") {
		t.Fatalf("log: %s %v", log, err)
	}
}

// fakeOpUser plays a signed-in person's op: one vault, service accounts with
// dummy tokens, and `user get --me` for a token. it logs argv.
const fakeOpUser = `#!/bin/sh
echo "$*" >> "$OP_LOG"
case "$1 $2" in
"--version "*) echo 2.34.0 ;;
"account list") echo '[{"url":"example.1password.com"}]' ;;
"vault list") echo '[]' ;;
"vault create") ;;
"service-account create") echo ops_DUMMYe2eToken ;;
"user get") [ -n "$OP_SERVICE_ACCOUNT_TOKEN" ] && echo '{"type":"SERVICE_ACCOUNT"}' || exit 1 ;;
*) exit 1 ;;
esac
`

func TestSecretProvisionEndToEnd(t *testing.T) {
	f := newFleet(t, "testdata/fleet.json")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "op"), []byte(fakeOpUser), 0o755); err != nil {
		t.Fatal(err)
	}
	opLog := filepath.Join(bin, "argv")
	for i, kv := range f.env {
		if strings.HasPrefix(kv, "PATH=") {
			f.env[i] = "PATH=" + bin + ":" + strings.TrimPrefix(kv, "PATH=")
		}
	}
	f.env = append(f.env, "OP_LOG="+opLog)
	f.must("init", "--yes")
	// keep the token off the real keychain
	cfg := filepath.Join(f.home, "yonder", "config", "config.toml")
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, append(data, []byte("\n[secrets]\ntoken_store = \"file\"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	r := f.must("secret", "setup", "--machines", "laptop", "--yes")
	if !strings.Contains(r.stdout, `laptop: ok, service account yonder-laptop reads "Yonder Agents"`) {
		t.Fatalf("setup: %+v", r)
	}
	if r := f.must("_secret-probe", "--verify"); !strings.Contains(r.stdout, `"verified":true`) || !strings.Contains(r.stdout, `"service_account":"yonder-laptop"`) {
		t.Fatalf("probe: %+v", r)
	}
	if r := f.run("", "secret", "setup", "--machines", "laptop", "--yes"); r.code != 0 || !strings.Contains(r.stdout, "--replace") {
		t.Fatalf("rerun without --replace: %+v", r)
	}
	if r := f.run("", "secret", "setup", "--provider", "bws", "--machines", "laptop"); r.code != 2 {
		t.Fatalf("--provider with --machines: %+v", r)
	}
	argv, _ := os.ReadFile(opLog)
	logData, _ := os.ReadFile(filepath.Join(f.home, "yonder", "state", "log.jsonl"))
	if strings.Contains(string(argv)+string(logData)+r.stdout+r.stderr, "ops_DUMMY") {
		t.Fatalf("token leaked:\n%s\n%s", argv, logData)
	}
	if strings.Count(string(argv), "service-account create yonder-laptop --vault Yonder Agents:read_items --raw") != 1 {
		t.Fatalf("op argv:\n%s", argv)
	}
}
