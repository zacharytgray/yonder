package remote

import (
	"bytes"
	"context"
	"io"
	"testing"
)

// B1: provider tokens in yonder's own env never reach the command.
func TestRunCommandDropsProviderVars(t *testing.T) {
	setup(t)
	t.Setenv("HOME", t.TempDir())
	vars := []string{"OP_SERVICE_ACCOUNT_TOKEN", "BWS_ACCESS_TOKEN", "YONDER_OP_TOKEN", "OP_CONNECT_TOKEN", "OP_CONNECT_HOST", "OP_SESSION_example"}
	for _, v := range vars {
		t.Setenv(v, "PROVIDER_TOKEN_CANARY")
	}
	t.Setenv("KEEP_ME", "kept")
	cmd := "printenv KEEP_ME; env | grep CANARY; true"
	var out bytes.Buffer
	if code, err := RunCommand(context.Background(), Envelope{Command: cmd}, nil, &out, io.Discard); code != 0 || err != nil {
		t.Fatalf("code %d err %v", code, err)
	}
	if out.String() != "kept\n" {
		t.Fatalf("got %q", out.String())
	}

	// asked for by name, the resolved value goes in, not the inherited one
	out.Reset()
	env := Envelope{Command: "printenv BWS_ACCESS_TOKEN; env | grep CANARY; true", Secrets: map[string]string{"BWS_ACCESS_TOKEN": "from-the-vault"}}
	if code, _ := RunCommand(context.Background(), env, nil, &out, io.Discard); code != 0 || out.String() != "[BWS_ACCESS_TOKEN]\n" {
		t.Fatalf("code %d got %q", code, out.String())
	}
}
