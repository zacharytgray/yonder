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

// fakeOpVault plays op for the agent vault. it logs argv to argv and each
// call's stdin to stdin.<n>, and keeps the created item in created.json.
const fakeOpVault = `#!/bin/sh
n=$(cat "$OP_STATE/n" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$OP_STATE/n"
echo "$*" >> "$OP_STATE/argv"
cat > "$OP_STATE/stdin.$n"
case "$1 $2" in
"--version "*) echo 2.34.0 ;;
"whoami "*|"account list") exit 0 ;;
"item list")
  if [ -f "$OP_STATE/created.json" ]; then
    echo '[{"id":"item1","title":"NEW_KEY","category":"API_CREDENTIAL","vault":{"id":"vault1","name":"Agents"}},{"id":"item2","title":"my notes","category":"SECURE_NOTE","vault":{"id":"vault1","name":"Agents"}}]'
  else echo '[]'; fi ;;
"item create") cp "$OP_STATE/stdin.$n" "$OP_STATE/created.json" ;;
"item get") cat "$OP_STATE/created.json" ;;
"item edit") cp "$OP_STATE/stdin.$n" "$OP_STATE/edited.json" ;;
"inject "*) sed -E 's/[{][{] op:[/][/][^}]* [}][}]/dummy-value-123/g' "$OP_STATE/stdin.$n" ;;
*) echo "[ERROR] 2026/11/11 20:00:00 unsupported" >&2; exit 1 ;;
esac
`

func TestSecretSetEndToEnd(t *testing.T) {
	const value, value2 = "sk-new-secret-value", "sk-replaced-value-2"
	f := newFleet(t, "testdata/fleet.json")
	bin, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "op"), []byte(fakeOpVault), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, kv := range f.env {
		if strings.HasPrefix(kv, "PATH=") {
			f.env[i] = "PATH=" + bin + ":" + strings.TrimPrefix(kv, "PATH=")
		}
	}
	f.env = append(f.env, "OP_STATE="+state)
	f.must("init", "--yes")
	f.must("secret", "setup", "--provider", "1password", "--mode", "desktop", "--vault", "Agents")
	var seen strings.Builder
	keep := func(r result) result { seen.WriteString(r.stdout + r.stderr); return r }

	if r := keep(f.run(value+"\n", "secret", "set", "NEW_KEY")); r.code != 0 || r.stdout != "stored NEW_KEY in Agents (19 chars)\n" {
		t.Fatalf("set: %+v", r)
	}
	created, _ := os.ReadFile(filepath.Join(state, "created.json"))
	if !strings.Contains(string(created), `"value":"`+value+`"`) || !strings.Contains(string(created), `"title":"NEW_KEY"`) {
		t.Fatalf("created item: %s", created)
	}
	if r := keep(f.run(value2+"\n", "secret", "set", "NEW_KEY")); r.code != 1 || !strings.Contains(r.stderr, "already exists") || !strings.Contains(r.stderr, "--replace") {
		t.Fatalf("set existing: %+v", r)
	}
	if r := keep(f.run(value2, "secret", "set", "NEW_KEY", "--replace")); r.code != 0 || r.stdout != "stored NEW_KEY in Agents (19 chars)\n" {
		t.Fatalf("set --replace: %+v", r)
	}
	edited, _ := os.ReadFile(filepath.Join(state, "edited.json"))
	if !strings.Contains(string(edited), `"value":"`+value2+`"`) {
		t.Fatalf("edited item: %s", edited)
	}

	r := keep(f.must("secret", "ls"))
	if !strings.Contains(r.stdout, "NEW_KEY  vault item") || !strings.Contains(r.stdout, "vault: Agents (read and write)") || !strings.Contains(r.stdout, "1 vault item(s) not listed") {
		t.Fatalf("ls: %s", r.stdout)
	}
	if r := keep(f.must("secret", "ls", "--json")); !strings.Contains(r.stdout, `"source": "vault"`) || !strings.Contains(r.stdout, `"vault": "Agents"`) {
		t.Fatalf("ls --json: %s", r.stdout)
	}
	if r := keep(f.must("secret", "test", "NEW_KEY")); r.stdout != "NEW_KEY: ok, 15 chars\n" {
		t.Fatalf("test: %+v", r)
	}
	if r := keep(f.must("run", "laptop", "--secret", "NEW_KEY", "--", "echo $NEW_KEY")); r.stdout != "[NEW_KEY]\n" {
		t.Fatalf("run: %+v", r)
	}
	if r := keep(f.run("", "secret", "set", "NEW_KEY")); r.code != 1 || !strings.Contains(r.stderr, "no value on stdin") {
		t.Fatalf("empty stdin: %+v", r)
	}
	if r := keep(f.run("x", "secret", "set", "new-key")); r.code != 2 {
		t.Fatalf("bad name: %+v", r)
	}
	f.must("secret", "setup", "--provider", "1password", "--extra-vault", "Ops")
	f.must("secret", "setup", "--provider", "1password") // no flag keeps it
	if r := keep(f.must("secret", "ls")); !strings.Contains(r.stdout, "extra vault: Ops (read-only)") || !strings.Contains(r.stdout, "NEW_KEY  item in Agents") {
		t.Fatalf("ls with an extra vault: %s", r.stdout)
	}
	if r := keep(f.must("secret", "ls", "--json")); !strings.Contains(r.stdout, `"extra_vaults": [`) || !strings.Contains(r.stdout, `"vault": "Ops"`) {
		t.Fatalf("ls --json with an extra vault: %s", r.stdout)
	}
	if r := keep(f.must("doctor", "--json")); !strings.Contains(r.stdout, `also reads \"Ops\" (read-only)`) {
		t.Fatalf("doctor: %s", r.stdout)
	}
	if r := keep(f.run("", "secret", "setup", "--provider", "bws", "--extra-vault", "Ops")); r.code == 0 || !strings.Contains(r.stderr, "only applies to 1password") {
		t.Fatalf("bws extra vault: %+v", r)
	}
	if r := keep(f.run("x", "secret", "set", "NEW_KEY", "--replace")); r.code != 0 || r.stdout != "stored NEW_KEY in Agents (1 chars)\n" {
		t.Fatalf("set with an extra vault: %+v", r)
	}
	f.must("secret", "setup", "--provider", "bws")
	if r := keep(f.run("x", "secret", "set", "OTHER")); r.code != 1 || !strings.Contains(r.stderr, "can't store secrets in Bitwarden") {
		t.Fatalf("bws: %+v", r)
	}

	argv, _ := os.ReadFile(filepath.Join(state, "argv"))
	if !strings.Contains(string(argv), "item create --vault Agents --tags yonder -\n") || !strings.Contains(string(argv), "item edit item1 --vault vault1\n") {
		t.Fatalf("op argv:\n%s", argv)
	}
	ins, _ := filepath.Glob(filepath.Join(state, "stdin.*"))
	byTitle := false
	for _, p := range ins {
		b, _ := os.ReadFile(p)
		byTitle = byTitle || strings.Contains(string(b), "{{ op://vault1/item1/credential }}")
	}
	if !byTitle {
		t.Fatal("NEW_KEY should resolve from the vault item by title")
	}
	logData, _ := os.ReadFile(filepath.Join(f.home, "yonder", "state", "log.jsonl"))
	for name, s := range map[string]string{"op argv": string(argv), "yonder output": seen.String(), "log.jsonl": string(logData)} {
		if strings.Contains(s, value) || strings.Contains(s, value2) {
			t.Fatalf("a value leaked into %s:\n%s", name, s)
		}
	}
}

// desktop-mode op inherits session and connect credentials and can echo
// them on stderr; none of it reaches vault_error.
const fakeOpLeaky = `#!/bin/sh
echo "connect token $OP_CONNECT_TOKEN" >&2
echo "OP_SESSION_example=$OP_SESSION_example" >&2
exit 1
`

func TestSecretLsScrubsInheritedProviderCreds(t *testing.T) {
	f := newFleet(t, "testdata/fleet.json")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "op"), []byte(fakeOpLeaky), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, kv := range f.env {
		if strings.HasPrefix(kv, "PATH=") {
			f.env[i] = "PATH=" + bin + ":" + strings.TrimPrefix(kv, "PATH=")
		}
	}
	f.env = append(f.env, "OP_SESSION_example=SESSION_CREDENTIAL_CANARY", "OP_CONNECT_TOKEN=CONNECT_CREDENTIAL_CANARY")
	f.must("init", "--yes")
	f.must("secret", "setup", "--provider", "1password", "--mode", "desktop", "--vault", "Agents")
	r := f.must("secret", "ls", "--json")
	if strings.Contains(r.stdout+r.stderr, "CANARY") || !strings.Contains(r.stdout, `"vault_error": "op item list`) || !strings.Contains(r.stdout, "[OP_SESSION_example]") {
		t.Fatalf("ls --json: %+v", r)
	}
}
