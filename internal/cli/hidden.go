package cli

import (
	"sort"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/remote"
)

// hiddenCmds are the commands callers run on a target over ssh.
func hiddenCmds() []*cobra.Command {
	names := make([]string, 0, len(remote.Commands))
	for n := range remote.Commands {
		names = append(names, n)
	}
	sort.Strings(names)
	var cmds []*cobra.Command
	for _, name := range names {
		cmds = append(cmds, &cobra.Command{
			Use:                name,
			Hidden:             true,
			DisableFlagParsing: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				code := remote.Dispatch(cmd.Context(), append([]string{name}, args...),
					cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
				return quietExit(code)
			},
		})
	}
	return cmds
}
