package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDeploysLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Print the log for a deploy run",
		Long:  `Print the plain-text log for a specific deploy run.`,
		Args:  exactArg("run-id", "see 'deeplo deploys history' for run IDs"),
		RunE: func(cmd *cobra.Command, args []string) error {
			log, err := daemonClient().RunLog(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), log) //nolint:errcheck
			return nil
		},
	}
}
