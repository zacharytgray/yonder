package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/registry"
	"github.com/zacharytgray/yonder/internal/remote"
	"github.com/zacharytgray/yonder/internal/runlog"
	"github.com/zacharytgray/yonder/internal/secrets"
)

func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage secrets and this machine's secrets provider",
		Long: `Manage secrets and this machine's secrets provider.

A secret NAME resolves from secrets.toml first, then from the 1Password item titled
exactly NAME in the agent vault. secrets.toml maps NAME to a reference
(op://vault/item/field or bws://<uuid>), never a value; it is per machine and not
synced. Vault items work by name on every machine that reads the vault.
` + "`yonder secret set NAME`" + ` stores a new one there from stdin or a hidden prompt.
` + "`yonder run m --secret NAME`" + ` resolves NAME here and passes it to the command's
environment on m. Output is scrubbed to [NAME].
` + "`yonder secret read NAME`" + ` prints a value, for programs that need a key on stdout.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newSecretAddCmd(), newSecretSetCmd(), newSecretLsCmd(), newSecretRmCmd(), newSecretTestCmd(), newSecretReadCmd(), newSecretSetupCmd(), newSecretTokenCmd())
	return cmd
}

func newSecretAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add NAME <ref>",
		Short: "Add or replace a reference (op://vault/item/field or bws://<uuid>)",
		Args:  usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, ref := args[0], args[1]
			if err := secrets.ValidName(name); err != nil {
				return &usageError{err}
			}
			if err := secrets.ValidRef(ref); err != nil {
				return &usageError{err}
			}
			if err := secrets.AddRef(name, ref); err != nil {
				return err
			}
			a := newApp(cmd)
			fmt.Fprintf(a.out, "added %s -> %s\n", name, ref)
			if s, err := secrets.LoadSettings(); err == nil {
				if s.Provider == "" {
					a.warnf("no secrets provider on this machine yet; run `yonder secret setup`")
				} else if !strings.HasPrefix(ref, schemeFor(s.Provider)) {
					a.warnf("this machine uses %s, which can't resolve %s references", s.Provider, strings.SplitN(ref, ":", 2)[0])
				}
			}
			return nil
		},
	}
}

func schemeFor(provider string) string {
	if provider == secrets.ProviderBWS {
		return "bws://"
	}
	return "op://"
}

func newSecretSetCmd() *cobra.Command {
	var replace bool
	cmd := &cobra.Command{
		Use:   "set NAME [--replace]",
		Short: "Store a secret in the agent vault (value from stdin or a hidden prompt)",
		Long: `Store a secret in the 1Password agent vault as an API Credential item titled NAME,
tagged yonder. The value comes from stdin when it isn't a terminal, else from a hidden
prompt. One trailing newline is dropped. It never appears in argv, output or the log.

  some-cli new-token | yonder secret set NAME
  yonder secret set NAME                 (hidden prompt)
  yonder secret set NAME --replace       (change an existing item's value)

Every machine that reads the vault can then use --secret NAME, with no mapping.
Desktop mode asks for one approval. A service account needs write access:
` + "`yonder secret setup --allow-write`" + `. Bitwarden is not supported.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := secrets.ValidName(name); err != nil {
				return &usageError{err}
			}
			a := newApp(cmd)
			if !a.store.LayerOn(registry.LayerSecrets) {
				return layerOff(registry.LayerSecrets, "secret set")
			}
			val, err := readSecretValue(a.in, name)
			if err != nil {
				return err
			}
			vault, err := secrets.Store(name, val, replace)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "stored %s in %s (%d chars)\n", name, vault, utf8.RuneCountInString(val))
			return nil
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, "change the value of an existing item with this title")
	return cmd
}

// readSecretValue: a hidden prompt on a terminal, else all of stdin minus
// one trailing newline.
func readSecretValue(in io.Reader, name string) (string, error) {
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		var v string
		err := huh.NewInput().Title("Value for " + name).Description("Hidden. It goes straight to 1Password.").
			EchoMode(huh.EchoModePassword).Value(&v).Run()
		if err != nil {
			return "", err
		}
		if v == "" {
			return "", errors.New("empty value; nothing stored")
		}
		return v, nil
	}
	b, err := io.ReadAll(io.LimitReader(in, secrets.MaxValueBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > secrets.MaxValueBytes {
		return "", fmt.Errorf("the value is over %d KiB; nothing stored", secrets.MaxValueBytes>>10)
	}
	v := string(b)
	if strings.HasSuffix(v, "\r\n") {
		v = v[:len(v)-2]
	} else {
		v = strings.TrimSuffix(v, "\n")
	}
	if v == "" {
		return "", fmt.Errorf("no value on stdin; pipe it in (some-cli new-token | yonder secret set %s) or run in a terminal", name)
	}
	return v, nil
}

type secretJSON struct {
	Name   string `json:"name"`
	Ref    string `json:"ref"`    // empty for vault items
	Source string `json:"source"` // "secrets.toml" or "vault"
	Vault  string `json:"vault"`  // the vault a vault item resolves from; empty for secrets.toml
}

func newSecretLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List secret names: secrets.toml references and vault titles (never values)",
		Long: `List the secret names this machine can resolve: references in secrets.toml, then
item titles that are valid names in the agent vault and then each extra vault. A name
resolves from the first of those that has it. Listing a vault reads titles only, never
values; in desktop mode it asks for approval like any read.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			s, err := secrets.LoadSettings()
			if err != nil {
				return err
			}
			refs, err := secrets.LoadRefs()
			if err != nil {
				return err
			}
			list := []secretJSON{}
			for _, n := range sortedNames(refs) {
				list = append(list, secretJSON{Name: n, Ref: refs[n], Source: "secrets.toml"})
			}
			lists, err := secrets.VaultNames()
			if err != nil {
				return err
			}
			vault, vaultErr, skipped := "", "", 0
			extras := []map[string]string{}
			seen := map[string]bool{}
			for i, l := range lists {
				e := ""
				if l.Err != nil {
					e = l.Err.Error()
				}
				if i == 0 {
					vault, vaultErr = l.Vault, e
				} else {
					extras = append(extras, map[string]string{"vault": l.Vault, "error": e})
				}
				skipped += l.Skipped
				for _, n := range l.Names {
					if _, mapped := refs[n]; !mapped && !seen[n] {
						seen[n] = true
						list = append(list, secretJSON{Name: n, Source: "vault", Vault: l.Vault})
					}
				}
			}
			sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
			if asJSON {
				enc := json.NewEncoder(a.out)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"provider": s.Provider, "mode": s.Mode, "vault": vault, "vault_error": vaultErr,
					"extra_vaults": extras, "secrets": list})
			}
			prov := "none (run `yonder secret setup`)"
			if s.Provider != "" {
				prov = s.String()
			}
			fmt.Fprintf(a.out, "provider: %s\n", prov)
			for i, l := range lists {
				if i == 0 {
					fmt.Fprintf(a.out, "vault: %s (%s)\n", l.Vault, s.Access())
				} else {
					fmt.Fprintf(a.out, "extra vault: %s (read-only)\n", l.Vault)
				}
				if l.Err != nil {
					a.warnf("could not list the vault %q: %v", l.Vault, l.Err)
				}
			}
			if len(list) == 0 {
				fmt.Fprintln(a.out, "no secrets yet; store one with `yonder secret set NAME` or map one with `yonder secret add NAME <ref>`")
				return nil
			}
			w := 0
			for _, e := range list {
				w = max(w, len(e.Name))
			}
			for _, e := range list {
				where := e.Ref
				if e.Source == "vault" {
					where = "vault item"
					if len(lists) > 1 {
						where = "item in " + e.Vault
					}
				}
				fmt.Fprintf(a.out, "%-*s  %s\n", w, e.Name, where)
			}
			if skipped > 0 {
				fmt.Fprintf(a.out, "(%d vault item(s) not listed: their titles aren't names like API_KEY)\n", skipped)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func newSecretRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME",
		Short: "Remove a reference",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := secrets.RemoveRef(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(newApp(cmd).out, "removed %s\n", args[0])
			return nil
		},
	}
}

func newSecretTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test NAME",
		Short: "Resolve a secret and print only its length",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := secrets.ValidName(name); err != nil {
				return &usageError{err}
			}
			a := newApp(cmd)
			if !a.store.LayerOn(registry.LayerSecrets) {
				return layerOff(registry.LayerSecrets, "secret test")
			}
			vals, err := secrets.Resolve([]string{name})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s: ok, %d chars\n", name, utf8.RuneCountInString(vals[name]))
			return nil
		},
	}
}

func newSecretReadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "read NAME",
		Short: "Print a secret's value, for programs that need a key on stdout",
		Long: `Print the value of NAME, then one newline, like ` + "`op read`" + `. It resolves on this
machine only, the same way --secret does here: secrets.toml, then the vaults this
machine reads. Desktop mode may ask for Touch ID.

This is for programs that need a key on stdout, such as an auth helper in a config file:

  auth_command = "yonder secret read MODEL_API_KEY"

Agents should use ` + "`yonder run <machine> --secret NAME`" + ` instead, which keeps the value
out of their output. The read is logged here by name, never the value.`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := secrets.ValidName(name); err != nil {
				return &usageError{err}
			}
			a := newApp(cmd)
			if !a.store.LayerOn(registry.LayerSecrets) {
				return layerOff(registry.LayerSecrets, "secret read")
			}
			start := time.Now()
			vals, err := secrets.Resolve([]string{name})
			if err == nil {
				_, err = fmt.Fprintln(a.out, vals[name])
			}
			a.logRead(name, start, err)
			return err
		},
	}
}

// logRead records a secret read in this machine's log, by name only.
func (a *app) logRead(name string, start time.Time, err error) {
	self := ""
	if c, cerr := a.store.Config(); cerr == nil {
		self = c.Self
	}
	e := runlog.Entry{
		Time: start.UTC(), ID: remote.NewID(), From: self, To: self, Harness: detectHarness(),
		User: localUser(), Command: "secret read " + name, Secrets: []string{name},
		Exit: runlog.Exit(ExitCode(err)), MS: time.Since(start).Milliseconds(),
	}
	if err != nil {
		e.Error = err.Error()
	}
	if werr := runlog.Append(registry.LogPath(), e); werr != nil {
		a.warnf("could not write log: %v", werr)
	}
}

func newSecretSetupCmd() *cobra.Command {
	var (
		provider, mode string
		yes            bool
		po             provisionOpts
	)
	cmd := &cobra.Command{
		Use:   "setup [--machines a,b | --all-machines] [--vault NAME] [--extra-vault NAME]... [--allow-write] [--replace] [--yes]",
		Short: "Set up secrets: hands-free 1Password for your machines, or this machine's provider",
		Long: `Set up secrets. With no flags it asks.

From a machine with the 1Password app (or an ` + "`op signin`" + ` session), yonder can set up
hands-free 1Password for the fleet in one run, with one approval: it creates a vault for
agent secrets (default "` + secrets.DefaultVault + `"), then one service account per machine,
named yonder-<machine>, and stores each token in that machine's keychain. The accounts
are read-only unless --allow-write, which lets agents store secrets with
` + "`yonder secret set`" + ` (and also change or delete items in that vault). The
tokens never touch disk here, argv or the log. Every machine needs ` + "`op`" + ` installed.
By default it sets up every other online machine, and this one only if it has no
1Password app. 1Password has no command to revoke a service account; do that at
` + secrets.RevokeWhere + `.

  yonder secret setup --yes                    the default machines, no prompts
  yonder secret setup --machines gpu-box       just these (offline ones: run this later)
  yonder secret setup --all-machines --vault Agents --expires-in 90d
  yonder secret setup --machines gpu-box --replace --allow-write
  yonder secret setup --machines server --vault Agents --extra-vault Ops --replace

--extra-vault (repeatable) also lets each new account read that vault, never write it.
Names resolve from the agent vault first, then each extra vault in order;
` + "`yonder secret set`" + ` writes only to the agent vault. Use it for secrets only some
machines should read.

Or choose this machine's provider by hand:
  1password desktop: the 1Password app approves reads with Touch ID or system auth.
  1password service-account: hands-free, a read-only token scoped to one dedicated vault.
  bws: Bitwarden Secrets Manager with a machine-account access token.
1Password cannot approve CLI reads from its phone app today.

Hand-chosen hands-free modes need ` + "`yonder secret token set`" + ` afterwards. With
--provider 1password, --vault NAME records the agent vault for ` + "`secret set`" + ` and
name lookups, and --extra-vault NAME (repeatable) more vaults to look names up in.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mode != "" && provider == "" {
				return &usageError{errors.New("--mode needs --provider")}
			}
			a := newApp(cmd)
			po.interactive = !yes && promptable()
			if provider != "" {
				if len(po.machines) > 0 || po.all || po.replace || po.expiresIn != "" || po.allowWrite {
					return &usageError{errors.New("--provider sets up this machine by hand; it doesn't mix with --machines, --all-machines, --replace, --expires-in or --allow-write")}
				}
			} else if po.set(yes) {
				if len(po.machines) > 0 && po.all {
					return &usageError{errors.New("use --machines or --all-machines, not both")}
				}
				return a.provision(po)
			} else if po.interactive {
				return a.secretsChoice(po, true)
			}
			return secrets.Setup(secrets.SetupOptions{
				Out:         a.out,
				Interactive: po.interactive,
				Force:       true,
				Provider:    provider,
				Mode:        mode,
				Vault:       po.vault,
				ExtraVaults: extrasOrNil(cmd, po.extraVaults),
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&provider, "provider", "", "set this machine by hand: 1password or bws (skips the prompts)")
	f.StringVar(&mode, "mode", "", "with --provider 1password: desktop or service-account")
	f.StringSliceVar(&po.machines, "machines", nil, "machines to give a service account, comma-separated")
	f.BoolVar(&po.all, "all-machines", false, "give every fleet machine a service account, this one included")
	f.StringVar(&po.vault, "vault", "", "1Password vault for agent secrets (default \""+secrets.DefaultVault+"\"; created if missing)")
	f.BoolVar(&po.replace, "replace", false, "replace a token a machine already has")
	f.StringVar(&po.expiresIn, "expires-in", "", "service accounts expire after this long, e.g. 90d (default: never)")
	f.StringArrayVar(&po.extraVaults, "extra-vault", nil, "also let the accounts read this vault, read-only (repeatable; created if missing)")
	f.BoolVar(&po.allowWrite, "allow-write", false, "let the service accounts write the vault too, so agents can `yonder secret set`")
	f.BoolVar(&yes, "yes", false, "never prompt; set up the default machines")
	return cmd
}

// extrasOrNil: nil when --extra-vault wasn't given, so a hand setup keeps
// the extra vaults it already has.
func extrasOrNil(cmd *cobra.Command, v []string) []string {
	if !cmd.Flags().Changed("extra-vault") {
		return nil
	}
	return v
}

// secretsChoice is the interactive start of setup: hands-free for the fleet
// when this machine can create service accounts, else the provider prompts.
// force asks even if this machine already has a provider.
func (a *app) secretsChoice(po provisionOpts, force bool) error {
	if secrets.UserSession() != nil {
		return secrets.Setup(secrets.SetupOptions{Out: a.out, Interactive: true, Force: force})
	}
	if cur, err := secrets.LoadSettings(); err == nil && cur.Provider != "" && !force {
		fmt.Fprintf(a.out, "secrets: using %s; `yonder secret setup` changes it\n", cur)
		return nil
	}
	choice := "fleet"
	err := huh.NewSelect[string]().
		Title("Set up secrets").
		Description("Agents ask for secrets by name. Yonder resolves them on the machine the agent runs on\nand passes them to the target's environment. Values stay out of command lines, logs\nand accidental output; an agent that sets out to read one still can.").
		Options(
			huh.NewOption("hands-free 1Password for my machines: one vault, one service account each (one approval)", "fleet"),
			huh.NewOption("choose a provider for this machine only", "here"),
			huh.NewOption("skip for now", ""),
		).
		Value(&choice).
		Run()
	switch {
	case err != nil || choice == "":
		fmt.Fprintln(a.out, "secrets: skipped; `yonder secret setup` sets it up later")
		return nil
	case choice == "here":
		return secrets.Setup(secrets.SetupOptions{Out: a.out, Interactive: true, Force: true})
	}
	po.interactive = true
	return a.provision(po)
}

func newSecretTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage the access token for hands-free providers",
		Args:  usageArgs(cobra.NoArgs),
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "set",
		Short: "Store the provider token (hidden prompt, or read from stdin)",
		Long: `Store the service account or machine-account token in this machine's keychain
(macOS Keychain, Linux secret service, else a 0600 file). It is read from a hidden
prompt on a terminal, or from stdin: yonder secret token set < tokenfile`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a := newApp(cmd)
			tok, err := readToken(a.in)
			if err != nil {
				return err
			}
			kind, err := secrets.SetToken(tok)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "token stored in %s (%d chars)\n", kind, len(tok))
			return nil
		},
	})
	return cmd
}

func readToken(in io.Reader) (string, error) {
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		var tok string
		err := huh.NewInput().Title("Paste the token").Description("Hidden.").
			EchoMode(huh.EchoModePassword).Value(&tok).Run()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(tok), nil
	}
	b, err := io.ReadAll(io.LimitReader(in, 64<<10))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errors.New("no token on stdin; pipe it in (yonder secret token set < tokenfile) or run in a terminal")
	}
	return tok, nil
}
