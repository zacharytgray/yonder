package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/secrets"
)

// hands-free 1password for the fleet in one run: the person here approves
// once, yonder makes the vault and one read-only service account per machine,
// and hands each token to its machine inside the _exec envelope.

type provisionOpts struct {
	vault       string
	machines    []string
	all         bool
	replace     bool
	expiresIn   string
	interactive bool
}

// wantsProvision: any provisioning flag means the fleet flow, not the provider prompts.
func (o provisionOpts) set(yes bool) bool {
	return yes || len(o.machines) > 0 || o.all || o.replace || o.vault != "" || o.expiresIn != ""
}

type provTarget struct {
	m       registry.Machine
	self    bool
	probe   secrets.ProbeResult
	account string
	old     string // what is being replaced, for the revoke reminder
}

func rerunLine(name string) string {
	return "yonder secret setup --machines " + name
}

func (a *app) provision(o provisionOpts) error {
	self, err := a.selfName()
	if err != nil {
		return err
	}
	if o.vault != "" {
		if err := secrets.CheckVaultName(o.vault); err != nil {
			return &usageError{err}
		}
	}
	if o.expiresIn != "" {
		if err := secrets.CheckExpiresIn(o.expiresIn); err != nil {
			return &usageError{err}
		}
	}
	if err := secrets.UserSession(); err != nil {
		return fmt.Errorf("%v\nto set up a machine by hand instead: yonder secret setup --provider 1password --mode service-account, then yonder secret token set", err)
	}
	ms, err := a.store.Machines()
	if err != nil {
		return err
	}
	known := map[string]registry.Machine{}
	for _, m := range ms {
		known[m.Name] = m
	}
	for _, n := range o.machines {
		if _, ok := known[n]; !ok {
			return &usageError{fmt.Errorf("no machine named %s in the registry (known: %s)", n, strings.Join(slices.Sorted(maps.Keys(known)), ", "))}
		}
	}

	// why a machine can't be set up right now
	why := map[string]string{}
	var reachable []string
	for _, m := range ms {
		switch {
		case m.Name == self:
		case m.Pending:
			why[m.Name] = "not set up yet; run `yonder refresh " + m.Name + "` once it is online"
		case m.SSHHostKey == "":
			why[m.Name] = "no pinned host key; run `yonder add " + m.Name + "`"
		case !a.isOnline(m):
			why[m.Name] = "offline"
		}
		if _, no := why[m.Name]; !no {
			reachable = append(reachable, m.Name)
		}
	}
	var defaults []string
	for _, n := range reachable {
		if n != self || !secrets.HasDesktopApp() {
			defaults = append(defaults, n)
		}
	}

	var chosen []string
	switch {
	case len(o.machines) > 0:
		chosen = o.machines
	case o.all:
		for _, m := range ms {
			chosen = append(chosen, m.Name)
		}
	case o.interactive:
		if o.vault == "" {
			o.vault = secrets.DefaultVault
			if err := huh.NewInput().Title("1Password vault for agent secrets").
				Description("Yonder creates it if it doesn't exist. Every chosen machine can read all of it.").
				Validate(secrets.CheckVaultName).Value(&o.vault).Run(); err != nil {
				return err
			}
		}
		for _, m := range ms {
			if w, ok := why[m.Name]; ok {
				fmt.Fprintf(a.out, "%s: can't set up now (%s); later: %s\n", m.Name, w, rerunLine(m.Name))
			}
		}
		if chosen, err = pickProvisionMachines(self, reachable, defaults); err != nil {
			return err
		}
	default:
		chosen = defaults
		for _, m := range ms {
			if w, ok := why[m.Name]; ok {
				fmt.Fprintf(a.out, "%s: skipped, %s; later: %s\n", m.Name, w, rerunLine(m.Name))
			}
		}
	}
	if o.vault == "" {
		o.vault = secrets.DefaultVault
	}

	failed := false
	fail := func(name, msg, fix string) {
		failed = true
		fmt.Fprintf(a.out, "%s: failed, %s\n", name, msg)
		if fix != "" {
			fmt.Fprintf(a.out, "  fix: %s\n", fix)
		}
	}

	// check every machine before any account exists, so none is orphaned
	var targets []provTarget
	for _, n := range chosen {
		m := known[n]
		if w, ok := why[n]; ok {
			if len(o.machines) > 0 || o.all {
				fmt.Fprintf(a.out, "%s: skipped, %s; later: %s\n", n, w, rerunLine(n))
			}
			continue
		}
		t := provTarget{m: m, self: n == self, account: secrets.AccountName(n)}
		if t.self {
			t.probe = secrets.Probe(false)
		} else if t.probe, err = a.remoteProbe(m, self, false); err != nil {
			if ExitCode(err) == ExitTempErr {
				fmt.Fprintf(a.out, "%s: skipped, %v; later: %s\n", n, err, rerunLine(n))
			} else {
				fail(n, err.Error(), "")
			}
			continue
		}
		if t.probe.Op == "" {
			fmt.Fprintf(a.out, "%s: skipped, the 1Password CLI `op` is not installed there. Install it (%s), then: %s\n",
				n, secrets.OpInstallHint(osOf(m, t.probe)), rerunLine(n))
			continue
		}
		if has := describeExisting(t.probe); has != "" {
			t.old = t.probe.ServiceAccount
			if t.probe.Provider == secrets.ProviderBWS {
				t.old = "bws"
			}
			ok := o.replace
			if !ok && o.interactive {
				ok = a.askYesNo(fmt.Sprintf("%s %s. Replace it?", n, has), false)
			}
			if !ok {
				fmt.Fprintf(a.out, "%s: skipped, it %s; to replace it: %s --replace\n", n, has, rerunLine(n))
				continue
			}
			if t.old == t.account {
				// two accounts with one name would make revoking the old one a guess
				t.account += "-" + time.Now().Format("20060102-1504")
			}
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		fmt.Fprintln(a.out, "nothing to set up")
		a.keepDesktopHere(self, chosen)
		if failed {
			return quietExit(ExitError)
		}
		return nil
	}

	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.m.Name
	}
	fmt.Fprintf(a.out, "Agent secrets go in the 1Password vault %q. The agents on %s can read everything in it.\n", o.vault, strings.Join(names, ", "))
	fmt.Fprintln(a.out, "1Password asks you to approve once (Touch ID or your system password).")
	created, err := secrets.EnsureVault(o.vault)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(a.out, "created the vault %q\n", o.vault)
	} else {
		fmt.Fprintf(a.out, "using the vault %q\n", o.vault)
	}

	changed := false
	for _, t := range targets {
		if a.provisionOne(t, self, o) {
			changed = true
		} else {
			failed = true
		}
	}
	a.keepDesktopHere(self, chosen)
	if changed {
		a.syncFleet(self)
	}
	fmt.Fprintf(a.out, "To cut a machine off later, revoke its account at %s (yonder can't revoke it for you).\n", secrets.RevokeWhere)
	fmt.Fprintf(a.out, "next: put agent secrets in %q, then on each machine an agent runs from: yonder secret add API_KEY 'op://%s/<item>/<field>'\n", o.vault, o.vault)
	if failed {
		return quietExit(ExitError)
	}
	return nil
}

// provisionOne creates t's account, installs the token there and checks it.
// the token lives only in this function's memory and the envelope.
func (a *app) provisionOne(t provTarget, self string, o provisionOpts) bool {
	n := t.m.Name
	tok, err := secrets.CreateServiceAccount(t.account, o.vault, o.expiresIn)
	if err != nil {
		fmt.Fprintf(a.out, "%s: failed, %v\n  fix: check the 1Password app is unlocked, then: %s\n", n, err, rerunLine(n))
		return false
	}
	revokeFix := fmt.Sprintf("revoke %s at %s, then: %s", t.account, secrets.RevokeWhere, rerunLine(n))
	var kind string
	if t.self {
		kind, err = remote.ProvisionHere(tok, t.account, o.vault)
	} else {
		kind, err = a.remoteProvision(t.m, self, tok, t.account, o.vault)
	}
	if err != nil {
		msg := secrets.ScrubText(err.Error(), map[string]string{"TOKEN": tok})
		fmt.Fprintf(a.out, "%s: failed, created %s but could not store its token: %s\n  fix: %s\n", n, t.account, msg, revokeFix)
		return false
	}
	tok = ""
	if !t.self {
		if m, err := a.store.Machine(n); err == nil {
			m.SecretsAccount, m.UpdatedAt = t.account, time.Now().UTC()
			if err := a.store.SaveMachine(m); err != nil {
				a.warnf("recording %s's account: %v", n, err)
			}
		}
	}
	var p secrets.ProbeResult
	if t.self {
		p = secrets.Probe(true)
	} else if p, err = a.remoteProbe(t.m, self, true); err != nil {
		p.Detail = err.Error()
	}
	if !p.Verified {
		fmt.Fprintf(a.out, "%s: failed, token stored in %s but the check failed: %s\n  fix: run `yonder doctor` on %s; if the token is bad: %s\n", n, kind, p.Detail, n, revokeFix)
		return false
	}
	fmt.Fprintf(a.out, "%s: ok, service account %s reads %q, token in %s\n", n, t.account, o.vault, kind)
	if t.old != "" {
		old := t.old
		switch old {
		case "bws":
			fmt.Fprintf(a.out, "  it used bws before; revoke that machine account's access token in Bitwarden if nothing else uses it\n")
		default:
			fmt.Fprintf(a.out, "  revoke the old account %s at %s\n", old, secrets.RevokeWhere)
		}
	}
	return true
}

// keepDesktopHere: this machine has the app and a person, so unless it was
// chosen it reads through the app (touch id) rather than a token.
func (a *app) keepDesktopHere(self string, chosen []string) {
	if slices.Contains(chosen, self) || !secrets.HasDesktopApp() {
		return
	}
	cur, err := secrets.LoadSettings()
	if err != nil || cur.Provider != "" {
		return
	}
	s := secrets.Settings{Provider: secrets.Provider1Password, Mode: secrets.ModeDesktop, TokenStore: cur.TokenStore}
	if err := secrets.SaveSettings(s); err != nil {
		a.warnf("secrets here: %v", err)
		return
	}
	fmt.Fprintf(a.out, "%s: 1password (desktop), you approve reads here with Touch ID or system auth\n", self)
}

func describeExisting(p secrets.ProbeResult) string {
	switch {
	case p.Provider == secrets.ProviderBWS:
		return "uses Bitwarden (bws) for secrets"
	case p.Provider == secrets.Provider1Password && p.Mode == secrets.ModeServiceAccount && p.HasToken:
		if p.ServiceAccount != "" {
			return "already has a 1Password service account token (" + p.ServiceAccount + "); the old account keeps working until you revoke it on 1Password.com"
		}
		return "already has a 1Password service account token; the old account keeps working until you revoke it on 1Password.com"
	}
	return ""
}

func osOf(m registry.Machine, p secrets.ProbeResult) string {
	if p.OS != "" {
		return p.OS
	}
	return m.OS
}

func pickProvisionMachines(self string, reachable, defaults []string) ([]string, error) {
	if len(reachable) == 0 {
		return nil, nil
	}
	opts := make([]huh.Option[string], len(reachable))
	for i, n := range reachable {
		label := n
		if n == self {
			label += "  (this machine; leave it off to keep Touch ID here)"
		}
		opts[i] = huh.NewOption(label, n).Selected(slices.Contains(defaults, n))
	}
	var sel []string
	err := huh.NewMultiSelect[string]().
		Title("Which machines get a hands-free service account?").
		Description("Each gets its own read-only account, named yonder-<machine>.").
		Options(opts...).
		Value(&sel).
		Run()
	return sel, err
}

// tooOld: an older yonder there has no such hidden command.
func tooOld(code int, out string) bool {
	return code == ExitUsage && strings.Contains(out, "unknown command")
}

func upgradeErr(m registry.Machine) error {
	return fmt.Errorf("yonder on %s is too old for this; upgrade yonder on %s, then: %s", m.Name, m.Name, rerunLine(m.Name))
}

func (a *app) remoteProbe(m registry.Machine, self string, verify bool) (secrets.ProbeResult, error) {
	var p secrets.ProbeResult
	args := []string{"_secret-probe"}
	if verify {
		args = append(args, "--verify")
	}
	var out, errb bytes.Buffer
	code, err := a.execOn(m, self, yonderCmd(m, args...), nil, &out, &errb)
	if err != nil {
		return p, err
	}
	if tooOld(code, out.String()+errb.String()) {
		return p, upgradeErr(m)
	}
	if code != 0 {
		return p, fmt.Errorf("checking %s exited %d: %s", m.Name, code, lastLine(errb.String()))
	}
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		return p, fmt.Errorf("bad reply from %s: %w", m.Name, err)
	}
	return p, nil
}

func (a *app) remoteProvision(m registry.Machine, self, tok, account, vault string) (string, error) {
	var out, errb bytes.Buffer
	vals := map[string]string{secrets.ProvisionTokenEnv: tok}
	code, err := a.execWith(m, self, yonderCmd(m, "_secret-provision", account, vault), vals, nil, &out, &errb)
	if err != nil {
		return "", err
	}
	errText := secrets.ScrubText(errb.String(), vals)
	if tooOld(code, errText) {
		return "", upgradeErr(m)
	}
	if code != 0 {
		return "", errors.New(lastLine(errText))
	}
	kind, ok := strings.CutPrefix(lastLine(secrets.ScrubText(out.String(), vals)), "token stored in ")
	if !ok {
		kind = "its token store"
	}
	return kind, nil
}

// askYesNo asks on the terminal; tests swap it.
var askYesNo = func(question string, def bool) bool {
	ok := def
	if err := huh.NewConfirm().Title(question).Value(&ok).Run(); err != nil {
		return false
	}
	return ok
}

func (a *app) askYesNo(question string, def bool) bool { return askYesNo(question, def) }
