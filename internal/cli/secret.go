package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/secrets"
)

func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage secret references and this machine's secrets provider",
		Long: `Manage secret references and this machine's secrets provider.

secrets.toml maps NAME to a reference (op://vault/item/field or bws://<uuid>), never
a value. It is per machine and not synced: add the same names on each machine an
agent runs from. ` + "`yonder run m --secret NAME`" + ` resolves NAME here and passes it
to the command's environment on m. Output is scrubbed to [NAME].`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newSecretAddCmd(), newSecretLsCmd(), newSecretRmCmd(), newSecretTestCmd(), newSecretSetupCmd(), newSecretTokenCmd())
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

type secretJSON struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

func newSecretLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List secret names and references (never values)",
		Args:  usageArgs(cobra.NoArgs),
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
				list = append(list, secretJSON{Name: n, Ref: refs[n]})
			}
			if asJSON {
				enc := json.NewEncoder(a.out)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"provider": s.Provider, "mode": s.Mode, "secrets": list})
			}
			prov := "none (run `yonder secret setup`)"
			if s.Provider != "" {
				prov = s.String()
			}
			fmt.Fprintf(a.out, "provider: %s\n", prov)
			if len(list) == 0 {
				fmt.Fprintln(a.out, "no secrets yet; add one with `yonder secret add NAME <ref>`")
				return nil
			}
			w := 0
			for _, e := range list {
				w = max(w, len(e.Name))
			}
			for _, e := range list {
				fmt.Fprintf(a.out, "%-*s  %s\n", w, e.Name, e.Ref)
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
			vals, err := secrets.Resolve([]string{name})
			if err != nil {
				return err
			}
			fmt.Fprintf(newApp(cmd).out, "%s: ok, %d chars\n", name, utf8.RuneCountInString(vals[name]))
			return nil
		},
	}
}

func newSecretSetupCmd() *cobra.Command {
	var (
		provider, mode string
		yes            bool
		po             provisionOpts
	)
	cmd := &cobra.Command{
		Use:   "setup [--machines a,b | --all-machines] [--vault NAME] [--replace] [--yes]",
		Short: "Set up secrets: hands-free 1Password for your machines, or this machine's provider",
		Long: `Set up secrets. With no flags it asks.

From a machine with the 1Password app (or an ` + "`op signin`" + ` session), yonder can set up
hands-free 1Password for the fleet in one run, with one approval: it creates a vault for
agent secrets (default "` + secrets.DefaultVault + `"), then one read-only service account per
machine, named yonder-<machine>, and stores each token in that machine's keychain. The
tokens never touch disk here, argv or the log. Every machine needs ` + "`op`" + ` installed.
By default it sets up every other online machine, and this one only if it has no
1Password app. 1Password has no command to revoke a service account; do that at
` + secrets.RevokeWhere + `.

  yonder secret setup --yes                    the default machines, no prompts
  yonder secret setup --machines gpu-box       just these (offline ones: run this later)
  yonder secret setup --all-machines --vault Agents --expires-in 90d

Or choose this machine's provider by hand:
  1password desktop: the 1Password app approves reads with Touch ID or system auth.
  1password service-account: hands-free, a read-only token scoped to one dedicated vault.
  bws: Bitwarden Secrets Manager with a machine-account access token.
1Password cannot approve CLI reads from its phone app today.

Hand-chosen hands-free modes need ` + "`yonder secret token set`" + ` afterwards.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mode != "" && provider == "" {
				return &usageError{errors.New("--mode needs --provider")}
			}
			a := newApp(cmd)
			po.interactive = !yes && promptable()
			if provider != "" {
				if po.set(false) {
					return &usageError{errors.New("--provider sets up this machine by hand; it doesn't mix with --machines, --all-machines, --vault, --replace or --expires-in")}
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
	f.BoolVar(&yes, "yes", false, "never prompt; set up the default machines")
	return cmd
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
		Description("Agents ask for secrets by name. Yonder resolves them on the machine the agent runs on\nand passes them to the target's environment. The model never sees a value.").
		Options(
			huh.NewOption("hands-free 1Password for my machines: one vault, one read-only service account each (one approval)", "fleet"),
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
