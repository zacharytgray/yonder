package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/agents"
	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/runlog"
	"github.com/zacharytgray/yonder/internal/secrets"
	"github.com/zacharytgray/yonder/internal/transport"
)

func newRemoveCmd() *cobra.Command {
	var lost, yes, forgetLocal, stripKeys, includeShared bool
	var stripFor string
	cmd := &cobra.Command{
		Use:   "remove <machine> [--lost] [--yes]",
		Short: "Remove a machine from the fleet",
		Long: `Remove a machine from the fleet. Yonder writes a tombstone and pushes it to every
online machine, so a later refresh never brings the machine back.

By default, if the machine is online, yonder also removes the skill there and clears its
copy of the registry. The yonder binary stays; the output says how to delete it.

--lost assumes the machine and its keys are compromised. Yonder does not contact it.
It removes the machine's ssh keys from ~/.ssh/authorized_keys on every reachable
machine (backup: authorized_keys.yonder-bak.<time>) and prints what else to revoke.
Run it again later to clean machines that were offline.
A key another fleet machine's record also lists is kept, and listed, unless
--include-shared is given: stripping it cuts that machine off too.

--forget-local clears this machine's own copy of the registry, for a machine that
was removed while it was offline.`,
		Args: usageArgs(func(cmd *cobra.Command, args []string) error {
			if forgetLocal || stripKeys {
				if len(args) != 0 {
					return fmt.Errorf("--forget-local takes no machine name")
				}
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("need one machine: yonder remove <machine> [--lost] [--yes]")
			}
			return nil
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			switch {
			case stripKeys:
				return a.stripKeysCommand(stripFor, includeShared)
			case forgetLocal:
				return a.forgetLocal(yes)
			}
			if includeShared && !lost {
				return &usageError{errors.New("--include-shared goes with --lost")}
			}
			return a.removeCommand(args[0], lost, yes, includeShared)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&lost, "lost", false, "the machine is lost or compromised: also strip its ssh keys everywhere")
	f.BoolVar(&yes, "yes", false, "don't ask for confirmation")
	f.BoolVar(&forgetLocal, "forget-local", false, "clear this machine's own registry copy and fleet name")
	f.BoolVar(&stripKeys, "strip-keys", false, "remove the public keys on stdin from ~/.ssh/authorized_keys")
	f.BoolVar(&includeShared, "include-shared", false, "with --lost: also strip keys other fleet machines use")
	f.StringVar(&stripFor, "for", "", "with --strip-keys: the lost machine, so keys other records list are kept")
	_ = f.MarkHidden("strip-keys")
	_ = f.MarkHidden("for")
	return cmd
}

func (a *app) ask(question string, yes bool) error {
	if yes {
		return nil
	}
	if !promptable() {
		return &usageError{errors.New("no terminal to confirm on; pass --yes")}
	}
	ok := false
	if err := huh.NewConfirm().Title(question).Value(&ok).Run(); err != nil {
		return &usageError{fmt.Errorf("could not ask for confirmation (%w); pass --yes", err)}
	}
	if !ok {
		return withCode(ExitError, "cancelled; nothing changed")
	}
	return nil
}

// promptable is a real terminal on stdin; /dev/null is a char device too.
func promptable() bool {
	if !isTerminal(os.Stdin) {
		return false
	}
	in, err1 := os.Stdin.Stat()
	null, err2 := os.Stat(os.DevNull)
	return err1 == nil && (err2 != nil || !os.SameFile(in, null))
}

func (a *app) removeCommand(name string, lost, yes, includeShared bool) error {
	self, err := a.selfName()
	if err != nil {
		return err
	}
	if err := registry.ValidName(name); err != nil {
		return &usageError{err}
	}
	if name == self {
		return fmt.Errorf("%s is this machine. Run `yonder remove %s` from another machine in the fleet; `yonder remove --forget-local` clears this machine's own copy of the registry", name, name)
	}
	m, recErr := a.store.Machine(name)
	tomb, hadTomb := a.store.Tombstone(name)
	keys := m.SSHClientKeys
	account := m.SecretsAccount
	rerun := false
	if errors.Is(recErr, registry.ErrNotFound) {
		switch {
		case hadTomb && lost && len(tomb.SSHClientKeys) > 0:
			rerun, keys, account = true, tomb.SSHClientKeys, tomb.SecretsAccount
		case hadTomb && lost:
			return fmt.Errorf("%s was removed on %s and yonder has no ssh keys on record for it; remove them from ~/.ssh/authorized_keys by hand", name, tomb.RemovedAt.Format("2006-01-02"))
		case hadTomb:
			return fmt.Errorf("%s was already removed on %s; `yonder remove %s --lost` also strips its ssh keys", name, tomb.RemovedAt.Format("2006-01-02"), name)
		default:
			_, err := a.machine(name)
			return err
		}
	} else if recErr != nil {
		return recErr
	}

	all, err := a.store.Machines()
	if err != nil {
		return err
	}
	var others, live []registry.Machine
	for _, p := range all {
		if p.Name != name {
			live = append(live, p)
			if p.Name != self {
				others = append(others, p)
			}
		}
	}
	targetOnline := !rerun && a.isOnline(m)
	// a key a live record also lists is shared: stripping it cuts that machine off too
	only, shared, users := remote.SplitShared(keys, live)
	strip := only
	if includeShared {
		strip = append(slices.Clone(only), shared...)
	}

	var plan []string
	switch {
	case rerun:
		plan = append(plan, fmt.Sprintf("strip the %d ssh key(s) only %s uses from authorized_keys here and on every online machine", len(only), name))
	case lost:
		plan = append(plan,
			"remove "+name+" from the registry here and on every online machine",
			fmt.Sprintf("strip the %d ssh key(s) only %s uses from authorized_keys here and on every online machine (it is not contacted)", len(only), name))
	default:
		if targetOnline {
			plan = append(plan, "remove the yonder skill on "+name+" and clear its copy of the registry")
		}
		plan = append(plan, "remove "+name+" from the registry here and on every online machine")
	}
	if lost && len(shared) > 0 {
		who := sharedWith(users)
		if includeShared {
			plan = append(plan, fmt.Sprintf("also strip the %d key(s) %s shares with %s (--include-shared); %s can't use them to reach the fleet afterwards:\n%s",
				len(shared), name, who, who, sharedList(shared, users)))
		} else {
			plan = append(plan, fmt.Sprintf("keep the %d key(s) %s shares with %s, since they use them too; --include-shared strips them as well:\n%s",
				len(shared), name, who, sharedList(shared, users)))
		}
	}
	fmt.Fprintf(a.out, "This will:\n")
	for _, p := range plan {
		fmt.Fprintf(a.out, "  - %s\n", p)
	}
	if err := a.ask("Remove "+name+"?", yes); err != nil {
		return err
	}
	start := time.Now()

	cleaned := false
	if !lost && targetOnline {
		var out bytes.Buffer
		cmd := yonderCmd(m, "agents", "uninstall") + " && " + yonderCmd(m, "remove", "--forget-local", "--yes")
		code, err := a.execOn(m, self, cmd, nil, &out, &out)
		switch {
		case err != nil:
			a.warnf("could not clean up %s: %v", name, err)
		case code != 0:
			a.warnf("cleanup on %s exited %d: %s", name, code, strings.TrimSpace(out.String()))
		default:
			cleaned = true
			fmt.Fprintf(a.out, "removed the skill on %s and cleared its registry copy\n", name)
		}
	}

	if !rerun {
		t := registry.Tombstone{Name: name, RemovedAt: time.Now().UTC().Truncate(time.Second), Reason: "removed", SSHClientKeys: keys, SecretsAccount: account}
		if lost {
			t.Reason, t.Lost = "lost", true
		}
		// a record stamped later than now (clock skew) would beat the tombstone.
		// date it just past added_at, but never past what peers accept
		if !m.AddedAt.Before(t.RemovedAt) {
			t.RemovedAt = m.AddedAt.Add(time.Second)
			if limit := time.Now().UTC().Add(registry.MaxSkew - time.Minute).Truncate(time.Second); t.RemovedAt.After(limit) {
				t.RemovedAt = limit
			}
		}
		if err := a.store.Remove(t); err != nil {
			return err
		}
		if m.Pending {
			fmt.Fprintf(a.out, "removed %s (it was never set up)\n", name)
		} else {
			fmt.Fprintf(a.out, "removed %s from the registry\n", name)
		}
	}
	// strip before the sync, so the explicit strip reports what it removed
	var stillTrust []string
	if lost {
		path := transport.AuthorizedKeysPath(registry.UserHome())
		n, backup, err := transport.StripAuthorizedKeys(path, strip)
		if err != nil {
			a.warnf("authorized_keys here: %v", err)
			stillTrust = append(stillTrust, self)
		} else {
			fmt.Fprintf(a.out, "%s: %s\n", self, stripSummary(n, backup))
		}
		for _, p := range others {
			if !a.isOnline(p) {
				stillTrust = append(stillTrust, p.Name)
				continue
			}
			args := []string{"remove", "--strip-keys", "--for", name}
			if includeShared {
				args = append(args, "--include-shared")
			}
			var out bytes.Buffer
			code, err := a.execOn(p, self, yonderCmd(p, args...), strings.NewReader(strings.Join(strip, "\n")+"\n"), &out, &out)
			msg := strings.TrimSpace(out.String())
			switch {
			case err != nil:
				a.warnf("%s: %v", p.Name, err)
				stillTrust = append(stillTrust, p.Name)
			case code != 0 && msg != "":
				a.warnf("%s: exit %d: %s", p.Name, code, msg)
				stillTrust = append(stillTrust, p.Name)
			case code != 0:
				a.warnf("%s: exit %d (an older yonder there?)", p.Name, code)
				stillTrust = append(stillTrust, p.Name)
			default:
				fmt.Fprintf(a.out, "%s: %s\n", p.Name, msg)
			}
		}
	}

	a.syncFleet(self)

	a.logRemoval(self, name, lost, start)

	var offline []string
	for _, p := range others {
		if !a.isOnline(p) && !p.Pending { // pending machines never got a registry copy
			offline = append(offline, p.Name)
		}
	}
	fmt.Fprintln(a.out)
	if len(offline) > 0 && !rerun {
		fmt.Fprintf(a.out, "Offline, so they still have %s's record: %s. They drop it at the next `yonder refresh` once they're online.\n",
			name, strings.Join(offline, ", "))
	}
	if lost {
		kept := shared
		if includeShared {
			kept = nil
		}
		a.lostChecklist(name, account, keys, stillTrust, kept, users)
		return nil
	}
	if m.Pending && !rerun {
		return nil // never got yonder, a registry copy or keys
	}
	switch {
	case cleaned:
		fmt.Fprintf(a.out, "yonder itself is still installed on %s. To delete it, run there: %s\n", name, uninstallLine(m.YonderPath))
	default:
		fmt.Fprintf(a.out, "%s wasn't reachable, so it still has the skill and its registry copy. When it's back, run there:\n  yonder agents uninstall && yonder remove --forget-local\n", name)
	}
	fmt.Fprintf(a.out, "Its ssh key is still trusted by the other machines. If it was lost or compromised, run `yonder remove %s --lost`.\n", name)
	if account != "" {
		fmt.Fprintf(a.out, "Its 1Password service account %s still works. Revoke it at %s.\n", account, secrets.RevokeWhere)
	}
	return nil
}

func uninstallLine(path string) string {
	if strings.Contains(path, "homebrew") || strings.Contains(path, "linuxbrew") || strings.Contains(path, "/Cellar/") {
		return "brew uninstall yonder"
	}
	if path == "" {
		path = "$(command -v yonder)"
	}
	return "rm " + transport.Quote(path)
}

// sharedWith names every machine that also lists one of the shared keys.
func sharedWith(users map[string][]string) string {
	var names []string
	for _, ns := range users {
		for _, n := range ns {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// sharedList is keyList with who else uses each key.
func sharedList(keys []string, users map[string][]string) string {
	lines := strings.Split(keyList(keys), "\n")
	for i, k := range keys {
		if id, ok := transport.KeyID(k); ok && i < len(lines) {
			lines[i] += "  (also " + strings.Join(users[id], ", ") + ")"
		}
	}
	return strings.Join(lines, "\n")
}

func (a *app) lostChecklist(name, account string, keys, stillTrust, kept []string, users map[string][]string) {
	fmt.Fprintf(a.out, "Now finish by hand:\n")
	revoke := "If it had a hands-free secrets token, revoke it. 1Password.com: Developer → Service accounts → its account → revoke the token. Bitwarden: Machine accounts → its account → Access tokens → revoke."
	if account != "" {
		revoke = "Revoke its 1Password service account " + account + ": https://start.1password.com/ → Developer → Service accounts → " + account + " → revoke its token. If it also had a Bitwarden token, revoke that under Machine accounts → Access tokens."
	}
	steps := []string{
		"Delete " + name + " from your tailnet: Tailscale admin console → Machines → " + name + " → Remove. Its tailnet key stops working at once.",
		revoke,
		"Rotate any secret that vault or account could read, and any API keys or tokens stored on " + name + " itself.",
		"Remove its ssh keys anywhere else that trusts them, such as GitHub or GitLab (Settings → SSH keys) and servers outside the fleet:\n" + keyList(keys),
	}
	if len(kept) > 0 {
		who := sharedWith(users)
		steps = append(steps, fmt.Sprintf("%s still holds %d key(s) that %s also use, so every machine still trusts them. Make new keys on %s, then run `yonder remove %s --lost --include-shared --yes`:\n%s",
			name, len(kept), who, who, name, sharedList(kept, users)))
	}
	if len(stillTrust) > 0 {
		steps = append(steps, "These machines still trust its key because yonder couldn't reach them: "+strings.Join(stillTrust, ", ")+
			". They strip it at the first `yonder refresh` that reaches them, or run `yonder remove "+name+" --lost --yes` again. `yonder doctor` flags them until then.")
	}
	steps = append(steps, "If you can, wipe the device itself (Find My for a Mac), and sign it out of the accounts it used.")
	for i, s := range steps {
		fmt.Fprintf(a.out, "  %d. %s\n", i+1, s)
	}
}

func keyList(keys []string) string {
	var b strings.Builder
	for _, k := range keys {
		f := strings.Fields(k)
		short := k
		if len(f) >= 2 {
			short = f[0] + " …" + tail(f[1], 12)
			if len(f) >= 3 {
				short += " " + strings.Join(f[2:], " ")
			}
		}
		b.WriteString("     " + short + "\n")
	}
	if len(keys) == 0 {
		b.WriteString("     (no keys on record)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func stripSummary(n int, backup string) string {
	home := registry.UserHome()
	if n == 0 {
		return "no matching keys in ~/.ssh/authorized_keys"
	}
	return fmt.Sprintf("removed %d line(s) from ~/.ssh/authorized_keys (backup: %s)", n, agents.Tilde(backup, home))
}

func (a *app) isOnline(m registry.Machine) bool {
	if m.Pending || m.SSHHostKey == "" {
		return false
	}
	_, err := a.pinned(m)
	return err == nil
}

// logRemoval records the removal in this machine's log as a caller-side entry.
func (a *app) logRemoval(self, name string, lost bool, start time.Time) {
	cmd := "yonder remove " + name
	if lost {
		cmd += " --lost"
	}
	e := runlog.Entry{
		Time: start.UTC(), ID: remote.NewID(), From: self, To: name, Harness: detectHarness(),
		User: localUser(), Command: cmd, Exit: runlog.Exit(0),
		MS: time.Since(start).Milliseconds(), Side: runlog.SideCaller,
	}
	if err := runlog.Append(registry.LogPath(), e); err != nil {
		a.warnf("could not write log: %v", err)
	}
}

func (a *app) forgetLocal(yes bool) error {
	if err := a.ask("Clear this machine's copy of the fleet registry?", yes); err != nil {
		return err
	}
	if err := a.store.Forget(); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "cleared this machine's registry copy and fleet name (other config kept)")
	return nil
}

// stripKeysCommand is the target side of `remove --lost`: keys on stdin, one per line.
// with lostName set, keys another record here lists are kept unless includeShared.
func (a *app) stripKeysCommand(lostName string, includeShared bool) error {
	var keys []string
	sc := bufio.NewScanner(io.LimitReader(a.in, 1<<20))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			keys = append(keys, l)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	var kept []string
	var users map[string][]string
	if lostName != "" && !includeShared {
		ms, err := a.store.Machines()
		if err != nil {
			return err
		}
		live := slices.DeleteFunc(ms, func(m registry.Machine) bool { return m.Name == lostName })
		keys, kept, users = remote.SplitShared(keys, live)
	}
	n, backup, err := transport.StripAuthorizedKeys(transport.AuthorizedKeysPath(registry.UserHome()), keys)
	if err != nil {
		return err
	}
	msg := stripSummary(n, backup)
	if len(kept) > 0 {
		msg += fmt.Sprintf("; kept %d key(s) %s also uses", len(kept), sharedWith(users))
	}
	fmt.Fprintln(a.out, msg)
	return nil
}
