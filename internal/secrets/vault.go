package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// the agent vault: `secret set` stores items titled NAME there, and a name
// missing from secrets.toml resolves from the item with that exact title.

// MaxValueBytes caps a value `secret set` will store.
const MaxValueBytes = 64 << 10

var errBWSWrite = errors.New("yonder can't store secrets in Bitwarden: `bws secret create` takes the value on its command line, where other programs can see it. Add it in the Bitwarden web app, then `yonder secret add NAME bws://<secret uuid>`")

var errNoVault = errors.New("no agent vault recorded on this machine; name it with `yonder secret setup --provider 1password --vault NAME`, or set up hands-free 1Password with `yonder secret setup`")

type vaultItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Vault    struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"vault"`
}

type opField struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// categoryField is the field to read for categories whose layout is fixed.
// other categories need the item's fields, so they cost one `op item get`.
func categoryField(category string) string {
	switch category {
	case "API_CREDENTIAL":
		return "credential"
	case "PASSWORD", "LOGIN":
		return "password"
	case "SECURE_NOTE":
		return "notesPlain"
	}
	return ""
}

// pickField: credential, then password, then notesPlain, then the first
// concealed field. -1 when there is none.
func pickField(fields []opField) int {
	for _, want := range []string{"credential", "password", "notesPlain"} {
		for i, f := range fields {
			if f.ID == want || f.Label == want {
				return i
			}
		}
	}
	for i, f := range fields {
		if f.Type == "CONCEALED" {
			return i
		}
	}
	return -1
}

// opCall runs op with this mode's env. msg is op's last stderr line.
func (o *onePassword) opCall(ctx context.Context, stdin []byte, args ...string) (out []byte, msg string, err error) {
	bin, err := lookPath("op")
	if err != nil {
		return nil, "", errors.New("1password CLI `op` not found; " + opInstallFix)
	}
	env, err := o.env()
	if err != nil {
		return nil, "", err
	}
	out, stderr, err := run(ctx, bin, args, env, stdin)
	if err != nil {
		return nil, errLine(stderr, err), err
	}
	return out, "", nil
}

// listItems lists titles, ids and categories. no values.
func (o *onePassword) listItems(ctx context.Context, vault string) ([]vaultItem, error) {
	out, msg, err := o.opCall(ctx, nil, "item", "list", "--vault", vault, "--format", "json")
	if msg != "" {
		return nil, fmt.Errorf("op item list --vault %q failed: %s", vault, msg)
	}
	if err != nil {
		return nil, err
	}
	var items []vaultItem
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, errors.New("op item list returned output yonder could not parse")
	}
	return items, nil
}

// getItem reads one item with its values; callers keep them in memory.
func (o *onePassword) getItem(ctx context.Context, id, vault string) ([]byte, error) {
	out, msg, err := o.opCall(ctx, nil, "item", "get", id, "--vault", vault, "--format", "json", "--reveal")
	if msg != "" {
		return nil, fmt.Errorf("op item get failed: %s", msg)
	}
	return out, err
}

func vaultOf(it vaultItem, vault string) string {
	if it.Vault.ID != "" {
		return it.Vault.ID
	}
	return vault
}

// titleNames are the item titles usable as secret names, sorted, and how many
// titles were not.
func titleNames(items []vaultItem) ([]string, int) {
	seen := map[string]bool{}
	var names []string
	skipped := 0
	for _, it := range items {
		if ValidName(it.Title) != nil {
			skipped++
			continue
		}
		if !seen[it.Title] {
			seen[it.Title] = true
			names = append(names, it.Title)
		}
	}
	sort.Strings(names)
	return names, skipped
}

// vaultLookup finds names among the vault's item titles with one list call.
// refs are for one op inject; vals come from items read whole, because
// their category doesn't say which field holds the secret.
func (o *onePassword) vaultLookup(vault string, names []string) (refs, vals map[string]string, inVault []string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	items, err := o.listItems(ctx, vault)
	if err != nil {
		return nil, nil, nil, err
	}
	inVault, _ = titleNames(items)
	byTitle := map[string][]vaultItem{}
	for _, it := range items {
		byTitle[it.Title] = append(byTitle[it.Title], it)
	}
	refs, vals = map[string]string{}, map[string]string{}
	for _, n := range names {
		its := byTitle[n]
		switch {
		case len(its) == 0:
			continue
		case len(its) > 1:
			return nil, nil, nil, fmt.Errorf("%d items in the vault %q are titled %s; rename or delete the extras in 1Password", len(its), vault, n)
		}
		it := its[0]
		if f := categoryField(it.Category); f != "" {
			refs[n] = "op://" + vaultOf(it, vault) + "/" + it.ID + "/" + f
			continue
		}
		raw, err := o.getItem(ctx, it.ID, vaultOf(it, vault))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", n, err)
		}
		var item struct {
			Fields []opField `json:"fields"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return nil, nil, nil, fmt.Errorf("%s: op item get returned output yonder could not parse (values not shown)", n)
		}
		i := pickField(item.Fields)
		if i < 0 {
			return nil, nil, nil, fmt.Errorf("%s: the item in %q has no credential, password, notes or concealed field to read", n, vault)
		}
		vals[n] = item.Fields[i].Value
	}
	return refs, vals, inVault, nil
}

// VaultNames lists the agent vault's item titles that are valid secret
// names, and how many titles were skipped. vault is "" when this machine
// has none; then nothing runs.
func VaultNames() (vault string, names []string, skipped int, err error) {
	s, err := LoadSettings()
	if err != nil || s.Provider != Provider1Password || s.Vault == "" {
		return "", nil, 0, err
	}
	p, err := newProvider(s)
	if err != nil {
		return s.Vault, nil, 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	items, err := p.(*onePassword).listItems(ctx, s.Vault)
	if err != nil {
		return s.Vault, nil, 0, err
	}
	names, skipped = titleNames(items)
	return s.Vault, names, skipped, nil
}

// Store saves value as the item titled name in the agent vault and returns
// the vault. the value goes to op only on stdin.
func Store(name, value string, replace bool) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	if value == "" {
		return "", errors.New("empty value; nothing stored")
	}
	if !utf8.ValidString(value) {
		return "", errors.New("the value isn't UTF-8 text, which 1Password needs; nothing stored")
	}
	if len(value) > MaxValueBytes {
		return "", fmt.Errorf("the value is over %d KiB; nothing stored", MaxValueBytes>>10)
	}
	s, err := LoadSettings()
	if err != nil {
		return "", err
	}
	switch {
	case s.Provider == "":
		return "", ErrNoProvider
	case s.Provider == ProviderBWS:
		return "", errBWSWrite
	case s.Vault == "":
		return "", errNoVault
	}
	p, err := newProvider(s)
	if err != nil {
		return "", err
	}
	return s.Vault, p.(*onePassword).store(s.Vault, name, value, replace)
}

func (o *onePassword) store(vault, name, value string, replace bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	items, err := o.listItems(ctx, vault)
	if err != nil {
		return err
	}
	var match []vaultItem
	for _, it := range items {
		if it.Title == name {
			match = append(match, it)
		}
	}
	scrub := map[string]string{name: value}
	switch {
	case len(match) > 1:
		return fmt.Errorf("%d items in the vault %q are titled %s; rename or delete the extras in 1Password", len(match), vault, name)
	case len(match) == 1 && !replace:
		return fmt.Errorf("%s already exists in the vault %q; to change its value: yonder secret set %s --replace", name, vault, name)
	case len(match) == 1:
		return o.replaceValue(ctx, match[0], vault, name, value)
	}
	tmpl, err := json.Marshal(map[string]any{
		"title":    name,
		"category": "API_CREDENTIAL",
		"fields":   []opField{{ID: "credential", Type: "CONCEALED", Label: "credential", Value: value}},
	})
	if err != nil {
		return err
	}
	if _, msg, err := o.opCall(ctx, tmpl, "item", "create", "--vault", vault, "--tags", "yonder", "-"); err != nil {
		return o.writeErr("op item create", msg, err, scrub)
	}
	return nil
}

// replaceValue rewrites the chosen field of an existing item through piped
// JSON, keeping everything else as op returned it.
func (o *onePassword) replaceValue(ctx context.Context, it vaultItem, vault, name, value string) error {
	scrub := map[string]string{name: value}
	raw, err := o.getItem(ctx, it.ID, vaultOf(it, vault))
	if err != nil {
		return err
	}
	var typed struct {
		Fields []opField `json:"fields"`
	}
	var item map[string]any
	if json.Unmarshal(raw, &typed) != nil || json.Unmarshal(raw, &item) != nil {
		return errors.New("op item get returned output yonder could not parse (values not shown)")
	}
	i := pickField(typed.Fields)
	fields, _ := item["fields"].([]any)
	if i < 0 || i >= len(fields) {
		return fmt.Errorf("%s in %q has no credential, password, notes or concealed field to replace; edit it in 1Password", name, vault)
	}
	f, ok := fields[i].(map[string]any)
	if !ok {
		return errors.New("op item get returned output yonder could not parse (values not shown)")
	}
	f["value"] = value
	body, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if _, msg, err := o.opCall(ctx, body, "item", "edit", it.ID, "--vault", vaultOf(it, vault)); err != nil {
		return o.writeErr("op item edit", msg, err, scrub)
	}
	return nil
}

func (o *onePassword) writeErr(what, msg string, err error, scrub map[string]string) error {
	if msg == "" {
		return err
	}
	if o.mode == ModeServiceAccount && permissionDenied(msg) {
		acct := o.account
		if acct == "" {
			acct = "the one whose token this machine holds"
		}
		return fmt.Errorf("this machine's service account is read-only; run `yonder secret setup --allow-write --machines %s --replace` from a machine with the 1Password app (it makes a new account; revoke the old one: %s, at %s)", selfOrPlaceholder(), acct, RevokeWhere)
	}
	return fmt.Errorf("%s failed: %s", what, ScrubText(msg, scrub))
}

func permissionDenied(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{"permission", "forbidden", "403", "not allowed", "not authorized"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
