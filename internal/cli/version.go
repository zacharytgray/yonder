package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/zacharytgray/yonder/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the yonder version",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "yonder %s (%s/%s)\n", version.Version, runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
