package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/store"
	"github.com/zacharytgray/yonder/internal/tailnet"
)

func TestAcceptPromptEscapesThePath(t *testing.T) {
	a, _ := testApp(t, &fakeSSH{}, &tailnet.Status{})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.store.SetSelf("laptop"))
	must(a.store.SetLayer(registry.LayerAgents, true, "laptop"))
	s, err := store.Open(agents.StoreName, "laptop")
	must(err)
	rel, body := "hooks/x\x1b[2J.sh", "#!/bin/sh\n"
	b := store.Bundle{Store: agents.StoreName, From: "gpu-box",
		Files: map[string]store.FileState{rel: {Hash: hashHex(body), Size: int64(len(body)), Version: store.Vector{"gpu-box": 1}, Origin: store.Origin{Machine: "gpu-box", Time: time.Now()}}},
		Data:  map[string][]byte{rel: []byte(body)}}
	var in, out bytes.Buffer
	must(store.WriteBundle(&in, b))
	must(store.ServeRecv(s, &in, &out, "gpu-box"))

	oldTTY, oldAsk := acceptTTY, acceptAsk
	defer func() { acceptTTY, acceptAsk = oldTTY, oldAsk }()
	var asked []string
	acceptTTY = func() bool { return true }
	acceptAsk = func(q string) bool { asked = append(asked, q); return false }
	cmd := newAgentsAcceptCmd()
	var printed bytes.Buffer
	cmd.SetOut(&printed)
	cmd.SetErr(&printed)
	cmd.SetArgs(nil)
	must(cmd.Execute())
	if len(asked) != 1 || strings.Contains(asked[0], "\x1b") || !strings.Contains(asked[0], `x\x1b[2J.sh`) {
		t.Fatalf("prompt: %q", asked)
	}
	if strings.Contains(printed.String(), "\x1b") {
		t.Fatalf("output: %q", printed.String())
	}
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
