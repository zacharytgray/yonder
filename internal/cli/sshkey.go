package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSSHKeyCmd() *cobra.Command {
	var clearKey bool
	cmd := &cobra.Command{
		Use:   "ssh-key <machine> [path | --clear]",
		Short: "Set, show or clear the ssh key this machine uses for a peer",
		Long: `Some people reach a peer with one specific key (ssh -i ~/.ssh/id_ed25519_gpubox ...)
and have no ssh config for its tailnet address. Set that key here and yonder passes
-i <key> -o IdentitiesOnly=yes on every ssh, scp and ssh-copy-id call to that peer.

The path lives in this machine's config.toml under [ssh.<machine>] and is never synced:
each machine has its own keys. With no path, it prints the current setting.`,
		Args: usageArgs(cobra.RangeArgs(1, 2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd)
			if clearKey && len(args) == 2 {
				return &usageError{fmt.Errorf("give a path or --clear, not both")}
			}
			return a.sshKeyCommand(args, clearKey)
		},
	}
	cmd.Flags().BoolVar(&clearKey, "clear", false, "forget the key and use ssh's default identities again")
	return cmd
}

func (a *app) sshKeyCommand(args []string, clearKey bool) error {
	name := args[0]
	if _, err := a.machine(name); err != nil {
		return err
	}
	switch {
	case clearKey:
		if err := a.store.SetIdentity(name, ""); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s: no ssh key set; ssh uses its default identities\n", name)
		a.updateOwnMeshKeys()
	case len(args) == 2:
		if err := checkIdentity(args[1]); err != nil {
			return &usageError{err}
		}
		if err := a.store.SetIdentity(name, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s: ssh key set to %s (this machine only)\n", name, args[1])
		a.updateOwnMeshKeys()
	default:
		if id := a.store.Identity(name); id != "" {
			fmt.Fprintf(a.out, "%s: %s\n", name, id)
		} else {
			fmt.Fprintf(a.out, "%s: no ssh key set; ssh uses its default identities\n", name)
		}
	}
	return nil
}

// updateOwnMeshKeys keeps this machine's own record in step with its keys.
// other machines hold their own copy, which sync doesn't change.
func (a *app) updateOwnMeshKeys() {
	self, err := a.selfName()
	if err != nil || !a.syncOwnMeshKeys(self) {
		return
	}
	fmt.Fprintf(a.out, "the keys %s offers to the fleet changed; on each other machine, `yonder add %s` takes them, then `yonder mesh`\n", self, self)
}
