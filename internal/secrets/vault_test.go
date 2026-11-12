package secrets

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const vaultItems = `[
 {"id":"i-api","title":"API_KEY","category":"API_CREDENTIAL","vault":{"id":"v1","name":"Agents"}},
 {"id":"i-pw","title":"DB_PASS","category":"PASSWORD","vault":{"id":"v1","name":"Agents"}},
 {"id":"i-login","title":"SITE","category":"LOGIN","vault":{"id":"v1","name":"Agents"}},
 {"id":"i-note","title":"NOTE","category":"SECURE_NOTE","vault":{"id":"v1","name":"Agents"}},
 {"id":"i-srv","title":"SERVER_KEY","category":"SERVER","vault":{"id":"v1","name":"Agents"}},
 {"id":"i-doc","title":"EMPTY","category":"DOCUMENT","vault":{"id":"v1","name":"Agents"}},
 {"id":"i-d1","title":"DUP","category":"API_CREDENTIAL","vault":{"id":"v1","name":"Agents"}},
 {"id":"i-d2","title":"DUP","category":"API_CREDENTIAL","vault":{"id":"v1","name":"Agents"}},
 {"id":"i-map","title":"MAPPED","category":"API_CREDENTIAL","vault":{"id":"v1","name":"Agents"}},
 {"id":"i-x","title":"my notes","category":"SECURE_NOTE","vault":{"id":"v1","name":"Agents"}}
]`

// fakeVault plays op for the agent vault: list, get, inject, create, edit.
func fakeVault(createErr string) func(c call) (string, string, error) {
	inject := fakeInject(map[string]string{
		"op://v1/i-api/credential":  "api-value",
		"op://v1/i-pw/password":     "pw-value",
		"op://v1/i-login/password":  "login-value",
		"op://v1/i-note/notesPlain": "note-value",
		"op://example-vault/m/f":    "mapped-value",
	})
	return func(c call) (string, string, error) {
		if len(c.args) < 2 {
			return inject(c)
		}
		switch c.args[0] + " " + c.args[1] {
		case "item list":
			return vaultItems, "", nil
		case "item get":
			switch c.args[2] {
			case "i-srv":
				return `{"id":"i-srv","fields":[{"id":"url","type":"STRING","label":"URL","value":"https://example.com"},{"id":"x1","type":"CONCEALED","label":"admin","value":"srv-value"}]}`, "", nil
			case "i-doc":
				return `{"id":"i-doc","fields":[{"id":"u","type":"STRING","label":"user","value":"me"}]}`, "", nil
			case "i-api":
				return `{"id":"i-api","title":"API_KEY","tags":["keep"],"fields":[{"id":"notesPlain","type":"STRING","purpose":"NOTES","label":"notesPlain","value":"a note"},{"id":"credential","type":"CONCEALED","label":"credential","value":"old-value"}]}`, "", nil
			}
		case "item create", "item edit":
			if createErr != "" {
				return "", "[ERROR] 2026/11/11 20:00:00 " + createErr + "\n", errors.New("exit status 1")
			}
			return "", "", nil
		}
		return "", "", errors.New("exit status 1")
	}
}

func TestVaultResolveFieldPerCategory(t *testing.T) {
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeDesktop, Vault: "Agents"}, map[string]string{"MAPPED": "op://example-vault/m/f"})
	f := &fakeExec{handler: fakeVault("")}
	installFake(t, f)
	names := []string{"MAPPED", "API_KEY", "DB_PASS", "SITE", "NOTE", "SERVER_KEY"}
	got, err := Resolve(names)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"MAPPED": "mapped-value", "API_KEY": "api-value", "DB_PASS": "pw-value", "SITE": "login-value", "NOTE": "note-value", "SERVER_KEY": "srv-value"}
	for n, v := range want {
		if got[n] != v {
			t.Errorf("%s = %q, want %q", n, got[n], v)
		}
	}
	// one list, one get for the category without a fixed field, one inject for the rest
	var ops []string
	for _, c := range f.calls {
		ops = append(ops, strings.Join(c.args[:min(3, len(c.args))], " "))
	}
	if strings.Join(ops, "|") != "item list --vault|item get i-srv|inject" {
		t.Fatalf("op calls: %v", ops)
	}
	if !strings.Contains(f.calls[2].stdin, "{{ op://example-vault/m/f }}") {
		t.Fatal("secrets.toml wins over a vault title")
	}

	// all mapped: the vault isn't listed
	f.calls = nil
	if _, err := Resolve([]string{"MAPPED"}); err != nil || len(f.calls) != 1 {
		t.Fatalf("%v %d calls", err, len(f.calls))
	}

	for name, wantErr := range map[string]string{
		"NOPE":  "store one with `yonder secret set NAME`",
		"DUP":   "2 items in the vault \"Agents\" are titled DUP",
		"EMPTY": "no credential, password, notes or concealed field",
	} {
		_, err := Resolve([]string{name})
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	_, err = Resolve([]string{"NOPE"})
	if !strings.Contains(err.Error(), "known: API_KEY, DB_PASS, DUP, EMPTY, MAPPED, NOTE, SERVER_KEY, SITE)") {
		t.Fatalf("known names: %v", err)
	}
}

func TestPickField(t *testing.T) {
	for want, fields := range map[int][]opField{
		1:  {{ID: "password"}, {ID: "credential"}},
		0:  {{Label: "password"}, {ID: "notesPlain"}},
		2:  {{ID: "u"}, {ID: "x"}, {ID: "notesPlain"}},
		3:  {{ID: "u"}, {ID: "x"}, {ID: "y"}, {ID: "z", Type: "CONCEALED"}},
		-1: {{ID: "u", Type: "STRING"}},
	} {
		if got := pickField(fields); got != want {
			t.Errorf("%v: got %d want %d", fields, got, want)
		}
	}
}

func TestStoreCreatesThroughStdin(t *testing.T) {
	const value = "sk-new-value-4242"
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeDesktop, Vault: "Agents"}, nil)
	f := &fakeExec{handler: fakeVault("")}
	installFake(t, f)
	vault, err := Store("NEW_KEY", value, false)
	if err != nil || vault != "Agents" {
		t.Fatalf("%q %v", vault, err)
	}
	c := f.calls[len(f.calls)-1]
	if strings.Join(c.args, " ") != "item create --vault Agents --tags yonder -" {
		t.Fatalf("argv: %v", c.args)
	}
	var item struct {
		Title, Category string
		Fields          []opField
	}
	if err := json.Unmarshal([]byte(c.stdin), &item); err != nil {
		t.Fatal(err)
	}
	if item.Title != "NEW_KEY" || item.Category != "API_CREDENTIAL" || len(item.Fields) != 1 ||
		item.Fields[0].ID != "credential" || item.Fields[0].Type != "CONCEALED" || item.Fields[0].Value != value {
		t.Fatalf("template: %+v", item)
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.args, " "), value) {
			t.Fatalf("value in argv: %v", c.args)
		}
	}
}

func TestStoreExistingTitle(t *testing.T) {
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeDesktop, Vault: "Agents"}, nil)
	f := &fakeExec{handler: fakeVault("")}
	installFake(t, f)
	if _, err := Store("API_KEY", "new-value", false); err == nil || !strings.Contains(err.Error(), "yonder secret set API_KEY --replace") {
		t.Fatalf("got %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("only the list runs: %v", f.calls)
	}
	f.calls = nil
	if _, err := Store("API_KEY", "new-value", true); err != nil {
		t.Fatal(err)
	}
	c := f.calls[len(f.calls)-1]
	if strings.Join(c.args, " ") != "item edit i-api --vault v1" {
		t.Fatalf("argv: %v", c.args)
	}
	var item struct {
		Tags   []string
		Fields []opField
	}
	if err := json.Unmarshal([]byte(c.stdin), &item); err != nil {
		t.Fatal(err)
	}
	if item.Fields[1].Value != "new-value" || item.Fields[0].Value != "a note" || len(item.Tags) != 1 {
		t.Fatalf("edit json: %+v", item)
	}
	if _, err := Store("DUP", "x", true); err == nil || !strings.Contains(err.Error(), "titled DUP") {
		t.Fatalf("got %v", err)
	}
}

func TestStoreReadOnlyServiceAccount(t *testing.T) {
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeServiceAccount, ServiceAccount: "yonder-gpu-box", Vault: "Agents"}, nil)
	installTokens(t, &fakeTokens{tokens: map[string]string{"1password": "ops_example-token"}})
	installFake(t, &fakeExec{handler: fakeVault("You do not have permission to perform this action")})
	_, err := Store("NEW_KEY", "v", false)
	for _, want := range []string{"read-only", "yonder secret setup --allow-write", "revoke the old one: yonder-gpu-box"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q in %v", want, err)
		}
	}

	// any other failure is op's message, minus the value
	installFake(t, &fakeExec{handler: fakeVault("bad template near sk-secret-xyz")})
	if _, err := Store("NEW_KEY", "sk-secret-xyz", false); err == nil || strings.Contains(err.Error(), "sk-secret-xyz") || !strings.Contains(err.Error(), "[NEW_KEY]") {
		t.Fatalf("got %v", err)
	}
}

func TestStoreRefusals(t *testing.T) {
	setupHome(t, Settings{Provider: ProviderBWS}, nil)
	f := &fakeExec{handler: fakeVault("")}
	installFake(t, f)
	if _, err := Store("A", "v", false); err == nil || !strings.Contains(err.Error(), "can't store secrets in Bitwarden") {
		t.Fatalf("bws: %v", err)
	}
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeDesktop}, nil)
	if _, err := Store("A", "v", false); err == nil || !strings.Contains(err.Error(), "--vault NAME") {
		t.Fatalf("no vault: %v", err)
	}
	if _, err := Store("bad-name", "v", false); err == nil {
		t.Fatal("bad name")
	}
	if _, err := Store("A", "", false); err == nil {
		t.Fatal("empty value")
	}
	if len(f.calls) != 0 {
		t.Fatalf("op ran: %v", f.calls)
	}
}

func TestWriteAccessRecorded(t *testing.T) {
	setupHome(t, Settings{}, nil)
	f := &fakeExec{handler: func(c call) (string, string, error) { return dummyToken + "\n", "", nil }}
	installFake(t, f)
	if _, err := CreateServiceAccount("yonder-gpu-box", "Agents", "", true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls[0].args, " "); got != "service-account create yonder-gpu-box --vault Agents:read_items,write_items --raw" {
		t.Fatalf("argv: %s", got)
	}

	ft := &fakeTokens{tokens: map[string]string{}}
	installTokens(t, ft)
	if _, err := InstallServiceAccount(dummyToken, "yonder-gpu-box", "Agents", true); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadSettings()
	if !s.Write || s.Vault != "Agents" || s.Access() != "read and write" || !Probe(false).Write {
		t.Fatalf("%+v", s)
	}
	installFake(t, &fakeExec{handler: func(c call) (string, string, error) { return "2.34.0\n", "", nil }})
	if c := findCheck(t, Health(), "1password vault"); c.Detail != `"Agents", read and write` {
		t.Fatalf("%+v", c)
	}
	if _, err := InstallServiceAccount(dummyToken, "yonder-gpu-box", "Agents", false); err != nil {
		t.Fatal(err)
	}
	if c := findCheck(t, Health(), "1password vault"); c.Detail != `"Agents", read-only` {
		t.Fatalf("%+v", c)
	}
}

func findCheck(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s check in %+v", name, checks)
	return Check{}
}

func TestSetupVaultByHand(t *testing.T) {
	setupHome(t, Settings{Provider: Provider1Password, Mode: ModeDesktop}, nil)
	var out strings.Builder
	if err := Setup(SetupOptions{Out: &out, Provider: Provider1Password, Vault: "Agents"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := LoadSettings(); s.Vault != "Agents" || s.Mode != ModeDesktop {
		t.Fatalf("%+v", s)
	}
	if err := Setup(SetupOptions{Out: &out, Provider: Provider1Password, Vault: "Personal"}); err == nil {
		t.Fatal("reserved vault")
	}
	if err := Setup(SetupOptions{Out: &out, Provider: ProviderBWS, Vault: "Agents"}); err == nil {
		t.Fatal("--vault with bws")
	}
}
