package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func DeployCmd() *cobra.Command {
	var host string

	cmd := &cobra.Command{
		Use:   "deploy <project>",
		Short: "Force a deploy of a project at its current HEAD commit",
		Long: `deploy queues an immediate deploy for a project.

The deploy runs asynchronously in the daemon.
Use 'deeplo deploys history' to monitor progress.
Use 'deeplo deploys logs <run-id>' to inspect output.`,
		Args: exactArg("project", "see your config for project names"),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := daemonClient().Deploy(cmd.Context(), args[0], host)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Queued: %s\n", strings.Join(resp.Targets, ", ")) //nolint:errcheck
			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "deploy to this host only (default: all hosts)")
	return cmd
}
