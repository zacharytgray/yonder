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
