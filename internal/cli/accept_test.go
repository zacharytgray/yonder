package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/store"
	"github.com/zacharytgray/yonder/internal/tailnet"
)

// waiting sets up laptop with the agents layer on and these files waiting
// for review, as gpu-box sent them.
func waiting(t *testing.T, files map[string]string) *store.Store {
	t.Helper()
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
	b := store.Bundle{Store: agents.StoreName, From: "gpu-box", Files: map[string]store.FileState{}, Data: map[string][]byte{}}
	for rel, body := range files {
		b.Files[rel] = store.FileState{Hash: hashHex(body), Size: int64(len(body)), Exec: strings.HasPrefix(body, "#!"),
			Version: store.Vector{"gpu-box": 1}, Origin: store.Origin{Machine: "gpu-box", Time: time.Now().Add(-time.Hour)}}
		b.Data[rel] = []byte(body)
	}
	var in, out bytes.Buffer
	must(store.WriteBundle(&in, b))
	must(store.ServeRecv(s, &in, &out, "gpu-box"))
	return s
}

// fakeTerm swaps in a person at a terminal who picks these answers in turn.
func fakeTerm(t *testing.T, tty bool, picks ...string) *[]string {
	oldTTY, oldChoose, oldPage, oldAsk := acceptTTY, acceptChoose, acceptPage, acceptAsk
	t.Cleanup(func() { acceptTTY, acceptChoose, acceptPage, acceptAsk = oldTTY, oldChoose, oldPage, oldAsk })
	var asked []string
	acceptTTY = func() bool { return tty }
	acceptChoose = func(q string, _ []string) string {
		asked = append(asked, q)
		if len(picks) == 0 {
			return choiceNo
		}
		p := picks[0]
		picks = picks[1:]
		return p
	}
	acceptPage = func(w io.Writer, text string) { asked = append(asked, "page"); _, _ = w.Write([]byte(text)) }
	return &asked
}

func runAccept(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newAgentsAcceptCmd()
	var printed bytes.Buffer
	cmd.SetOut(&printed)
	cmd.SetErr(&printed)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return printed.String(), err
}

func TestAcceptSummaryEscapesThePath(t *testing.T) {
	waiting(t, map[string]string{"hooks/x\x1b[2J.sh": "#!/bin/sh\n"})
	asked := fakeTerm(t, true)
	out, err := runAccept(t)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\x1b") || !strings.Contains(out, `x\x1b[2J.sh`) || len(*asked) != 1 {
		t.Fatalf("summary: %q %q", out, *asked)
	}
	if !strings.Contains(out, "nothing accepted") {
		t.Fatalf("not now should accept nothing: %s", out)
	}
}

func TestAcceptAllOneConfirmation(t *testing.T) {
	s := waiting(t, map[string]string{
		"hooks/a.sh":             "#!/bin/sh\necho a\n",
		"hooks/b.sh":             "#!/bin/sh\necho b\n",
		"skills/x/run.sh":        "#!/bin/sh\nrun\n",
		"files/codex/hooks.json": "{}\n",
	})
	asked := fakeTerm(t, true, choiceShow, choiceAccept)
	out, err := runAccept(t, "--all")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"4 changes wait for review here", "hooks (2)", "scripts (1)", "harness files (1)", "from gpu-box", "new, 2 lines", "accepted 4 changes", "/hooks"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// one question, then the diffs, then the same question: one yes accepts all
	if len(*asked) != 3 || (*asked)[1] != "page" {
		t.Fatalf("asked %q", *asked)
	}
	if b, err := os.ReadFile(filepath.Join(s.Dir, "hooks", "a.sh")); err != nil || string(b) != "#!/bin/sh\necho a\n" {
		t.Fatalf("not live: %q %v", b, err)
	}
	if err := s.Lock(time.Second); err != nil {
		t.Fatal(err)
	}
	st, _ := s.State()
	s.Unlock()
	if st.Files["hooks/a.sh"].Reviewed != "laptop" {
		t.Errorf("accept should mark the version reviewed here: %+v", st.Files["hooks/a.sh"])
	}
}

func TestAcceptNeedsATerminal(t *testing.T) {
	waiting(t, map[string]string{"hooks/a.sh": "#!/bin/sh\n"})
	asked := fakeTerm(t, false, choiceAccept)
	out, err := runAccept(t, "--all")
	if err == nil || !strings.Contains(err.Error(), "yonder agents accept --all") || len(*asked) != 0 {
		t.Fatalf("no terminal: %v %q", err, *asked)
	}
	if !strings.Contains(out, "hooks/a.sh") {
		t.Fatalf("the summary still shows what waits: %s", out)
	}
	// --all-machines refuses before it asks any machine anything
	if _, err := runAccept(t, "--all-machines"); err == nil || !strings.Contains(err.Error(), "--all-machines") {
		t.Fatalf("all machines: %v", err)
	}
}

// what arrives after the summary was shown keeps waiting
func TestAcceptOnlyWhatWasShown(t *testing.T) {
	s := waiting(t, map[string]string{"hooks/a.sh": "#!/bin/sh\necho a\n"})
	items, err := listPending(s)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	items[0].Hash = hashHex("something else")
	a := &app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}
	d := a.acceptHere(s, items)
	if len(d.Accepted) != 0 || len(d.Skipped) != 1 || !strings.Contains(d.Skipped[0].Why, "changed since") {
		t.Fatalf("%+v", d)
	}
}

// a target accepts only what it listed, for the caller it listed it to, once
func TestAcceptTicket(t *testing.T) {
	waiting(t, map[string]string{"hooks/a.sh": "#!/bin/sh\necho a\n", "hooks/b.sh": "#!/bin/sh\necho b\n"})
	t.Setenv("SSH_CONNECTION", "")
	run := func(c interface {
		SetArgs([]string)
		SetOut(io.Writer)
		SetErr(io.Writer)
		SetIn(io.Reader)
		Execute() error
	}, stdin string) (string, error) {
		var out bytes.Buffer
		c.SetArgs(nil)
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetIn(strings.NewReader(stdin))
		err := c.Execute()
		return out.String(), err
	}
	out, err := run(newAcceptListCmd(), "")
	if err != nil {
		t.Fatal(err)
	}
	var l acceptList
	if err := json.Unmarshal([]byte(out), &l); err != nil || l.Ticket == "" || len(l.Items) != 2 {
		t.Fatalf("list: %s %v", out, err)
	}
	// a request for something it didn't list is skipped; the rest goes through
	want := acceptWant{Ticket: l.Ticket, Items: []wantItem{{l.Items[0].Path, l.Items[0].Hash, l.Items[0].Exec}, {"hooks/c.sh", "x", true}}}
	body, _ := json.Marshal(want)
	out, err = run(newAcceptApplyCmd(), string(body))
	if err != nil {
		t.Fatal(err)
	}
	var d acceptDone
	if err := json.Unmarshal([]byte(out), &d); err != nil || len(d.Accepted) != 1 || len(d.Skipped) != 1 {
		t.Fatalf("apply: %s %v", out, err)
	}
	// once
	if _, err := run(newAcceptApplyCmd(), string(body)); err == nil || !strings.Contains(err.Error(), "no such ticket") {
		t.Fatalf("a ticket must work once: %v", err)
	}
	// expired
	out, _ = run(newAcceptListCmd(), "")
	_ = json.Unmarshal([]byte(out), &l)
	ts := loadTickets()
	tk := ts[l.Ticket]
	tk.Created = time.Now().Add(-time.Hour)
	ts[l.Ticket] = tk
	if err := saveTickets(ts); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(acceptWant{Ticket: l.Ticket, Items: []wantItem{{l.Items[0].Path, l.Items[0].Hash, l.Items[0].Exec}}})
	if _, err := run(newAcceptApplyCmd(), string(body)); err == nil {
		t.Fatal("an expired ticket must not accept")
	}
	// another caller's ticket
	out, _ = run(newAcceptListCmd(), "")
	_ = json.Unmarshal([]byte(out), &l)
	ts = loadTickets()
	tk = ts[l.Ticket]
	tk.From = "gpu-box"
	ts[l.Ticket] = tk
	_ = saveTickets(ts)
	body, _ = json.Marshal(acceptWant{Ticket: l.Ticket, Items: []wantItem{{l.Items[0].Path, l.Items[0].Hash, l.Items[0].Exec}}})
	if _, err := run(newAcceptApplyCmd(), string(body)); err == nil || !strings.Contains(err.Error(), "given to gpu-box") {
		t.Fatalf("another caller's ticket: %v", err)
	}
}

func TestReviewKind(t *testing.T) {
	for rel, want := range map[string]string{
		"hooks/a.sh": "hooks", "machines/gpu-box/hooks/a.sh": "hooks", "files/codex/hooks.json": "harness files",
		"machines/laptop/files/claude/x.json": "harness files", "skills/x/run.sh": "scripts", "instructions.md": "other",
	} {
		if got := reviewKind(rel, strings.HasSuffix(rel, ".sh")); got != want {
			t.Errorf("%s: %s, want %s", rel, got, want)
		}
	}
}
