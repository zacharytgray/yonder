package secrets

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/huh"
)

// SetupOptions drives Setup. Provider set means no prompts.
type SetupOptions struct {
	Out         io.Writer
	Interactive bool
	Force       bool // ask again even if a provider is set
	Provider    string
	Mode        string
	Vault       string // 1password: the agent vault, for setups made by hand
}

// SetupInteractive runs Setup for init. it leaves an existing setup alone.
func SetupInteractive(interactive bool) error {
	return Setup(SetupOptions{Out: os.Stdout, Interactive: interactive})
}

// HasDesktopApp reports whether the 1Password app is installed here. a var for tests.
var HasDesktopApp = desktopAppInstalled

func desktopAppInstalled() bool {
	paths := []string{"/Applications/1Password.app"}
	if h, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(h, "Applications", "1Password.app"))
	}
	if runtime.GOOS == "linux" {
		paths = []string{"/opt/1Password/1password", "/usr/bin/1password"}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func headless() bool {
	if os.Getenv("SSH_CONNECTION") != "" {
		return true
	}
	return runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
}

// recommendedMode: desktop where the app lives and a person sits, else hands-free.
func recommendedMode() string {
	if HasDesktopApp() && !headless() {
		return ModeDesktop
	}
	return ModeServiceAccount
}

const modesExplained = `How 1Password modes work:
  desktop          op asks the 1Password app, which asks you (Touch ID, Apple Watch or
                   your system password). One approval lasts until 10 min idle, 12 h max.
                   Needs the app on this machine and someone at it.
  service-account  hands-free. A read-only 1Password service account token, stored in this
                   machine's keychain. It can only see vaults you grant it, and never your
                   Personal/Private vault, so make a vault just for this.
                   Easiest: from a machine with the 1Password app, run
                   ` + "`yonder secret setup --machines <names>`" + `. It makes the vault
                   and one service account per machine with one approval.
                   By hand: 1Password.com > Developer > Directory > Other >
                   Create a Service Account, then ` + "`yonder secret token set`" + `.
                   Individual and Families plans allow 1,000 reads a day.
1Password cannot approve CLI reads from its phone app today.`

const bwsExplained = `Bitwarden Secrets Manager: hands-free with a machine-account access token,
stored in this machine's keychain. References look like bws://<secret uuid>.`

func Setup(o SetupOptions) error {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	cur, err := LoadSettings()
	if err != nil {
		return err
	}
	if o.Provider != "" {
		s := Settings{Provider: o.Provider, Mode: o.Mode, TokenStore: cur.TokenStore}
		if s.Provider == Provider1Password && s.Mode == "" {
			s.Mode = recommendedMode()
			if cur.Provider == Provider1Password {
				s.Mode = cur.Mode
			}
		}
		if s.Provider == cur.Provider && s.Mode == cur.Mode {
			s.ServiceAccount, s.Write = cur.ServiceAccount, cur.Write
		}
		if s.Provider == cur.Provider {
			s.Vault = cur.Vault
		}
		if s.Provider != Provider1Password && s.Mode != "" {
			return fmt.Errorf("--mode only applies to 1password")
		}
		if o.Vault != "" {
			if s.Provider != Provider1Password {
				return fmt.Errorf("--vault only applies to 1password")
			}
			if err := CheckVaultName(o.Vault); err != nil {
				return err
			}
			s.Vault = o.Vault
		}
		return saveAndExplain(o.Out, s)
	}
	if cur.Provider != "" && !o.Force {
		fmt.Fprintf(o.Out, "secrets: using %s; `yonder secret setup` changes it\n", cur)
		return nil
	}
	if !o.Interactive {
		fmt.Fprintln(o.Out, "secrets: not set up. to use `yonder run --secret`, run `yonder secret setup`,")
		fmt.Fprintln(o.Out, "  or without prompts: yonder secret setup --provider 1password --mode service-account")
		fmt.Fprintln(o.Out, "  then: yonder secret token set < tokenfile")
		return nil
	}
	return setupPrompt(o.Out, cur)
}

func setupPrompt(w io.Writer, cur Settings) error {
	_, opErr := lookPath("op")
	_, bwsErr := lookPath("bws")
	opLabel, bwsLabel := "1Password (op)", "Bitwarden Secrets Manager (bws)"
	if opErr != nil {
		opLabel += "  - op not installed yet"
	}
	if bwsErr != nil {
		bwsLabel += "  - bws not installed yet"
	}
	provider := Provider1Password
	if opErr != nil && bwsErr == nil {
		provider = ProviderBWS
	}
	err := huh.NewSelect[string]().
		Title("Secrets provider for this machine").
		Description("Agents ask for secrets by name. Yonder resolves them here and passes them to the\ntarget's environment. The model never sees a value.").
		Options(
			huh.NewOption(opLabel, Provider1Password),
			huh.NewOption(bwsLabel, ProviderBWS),
			huh.NewOption("skip for now", ""),
		).
		Value(&provider).
		Run()
	if err != nil || provider == "" {
		fmt.Fprintln(w, "secrets: skipped; `yonder secret setup` sets it up later")
		return nil
	}
	s := Settings{Provider: provider, TokenStore: cur.TokenStore}
	if provider == Provider1Password {
		s.Mode = recommendedMode()
		rec := func(m string) string {
			if m == s.Mode {
				return "  (recommended here)"
			}
			return ""
		}
		err := huh.NewSelect[string]().
			Title("How should 1Password approve reads?").
			Description(modesExplained).
			Options(
				huh.NewOption("desktop: you approve with Touch ID or system auth"+rec(ModeDesktop), ModeDesktop),
				huh.NewOption("service-account: hands-free, scoped to one vault"+rec(ModeServiceAccount), ModeServiceAccount),
			).
			Value(&s.Mode).
			Run()
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintln(w, bwsExplained)
	}
	if err := SaveSettings(s); err != nil {
		return err
	}
	fmt.Fprintf(w, "secrets: using %s\n", s)
	if !s.HandsFree() {
		return explainNext(w, s)
	}
	what := "1Password service account token (starts with ops_)"
	if s.Provider == ProviderBWS {
		what = "bws machine-account access token"
	}
	var tok string
	err = huh.NewInput().
		Title("Paste the " + what).
		Description("Hidden. Leave empty to set it later with `yonder secret token set`.").
		EchoMode(huh.EchoModePassword).
		Value(&tok).
		Run()
	if err != nil {
		return err
	}
	if tok = strings.TrimSpace(tok); tok != "" {
		kind, err := SetToken(tok)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "secrets: token stored in %s\n", kind)
	}
	return explainNext(w, s)
}

func saveAndExplain(w io.Writer, s Settings) error {
	if err := validateSettings(s); err != nil {
		return err
	}
	if err := SaveSettings(s); err != nil {
		return err
	}
	fmt.Fprintf(w, "secrets: using %s\n", s)
	return explainNext(w, s)
}

func explainNext(w io.Writer, s Settings) error {
	if s.HandsFree() {
		tokens := newTokenStore(s.TokenStore)
		if _, err := tokens.Get(s.Provider); errors.Is(err, ErrNoToken) {
			fmt.Fprintf(w, "next: store the token with `yonder secret token set` (hidden prompt, or < tokenfile); it goes in the %s\n", tokens.Kind())
		}
	}
	if s.Provider == Provider1Password && s.Vault == "" {
		fmt.Fprintln(w, "next: to store secrets with `yonder secret set`, name the agent vault: yonder secret setup --provider 1password --vault NAME")
	}
	if s.Vault != "" {
		fmt.Fprintf(w, "secrets: agent vault %q (%s); `yonder secret set NAME` stores a secret there\n", s.Vault, s.Access())
	}
	fmt.Fprintf(w, "next: add references, e.g. `yonder secret add API_KEY %s`, then `yonder secret test API_KEY`\n", exampleRef(s.Provider))
	return nil
}

// SetToken stores the token for this machine's hands-free provider and says where.
func SetToken(token string) (string, error) {
	s, err := LoadSettings()
	if err != nil {
		return "", err
	}
	if s.Provider == "" {
		return "", ErrNoProvider
	}
	if !s.HandsFree() {
		return "", fmt.Errorf("%s does not use a stored token; switch with `yonder secret setup --provider 1password --mode service-account`", s)
	}
	if err := checkToken(token); err != nil {
		return "", err
	}
	store := newTokenStore(s.TokenStore)
	if err := store.Set(s.Provider, token); err != nil {
		return "", err
	}
	return store.Kind(), nil
}
