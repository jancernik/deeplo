package cli

import (
	"fmt"

	"github.com/jancernik/deeplo/internal/build"
	"github.com/spf13/cobra"
)

func VersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the deeplo version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "deeplo %s\n", build.Version) //nolint:errcheck
		},
	}
}
