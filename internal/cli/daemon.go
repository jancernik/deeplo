package cli

import (
	"github.com/jancernik/deeplo/internal/daemon"
	"github.com/spf13/cobra"
)

func NewDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the deployment daemon",
		Long: `daemon starts the deeplo deployment daemon.

The daemon is configured via DEEPLO_* environment variables.
In a native install it is managed by systemd (deeplo.service).
In Docker it runs as the container's default command.

You do not normally need to run this directly.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.New().Run(cmd.Context())
		},
	}
}
