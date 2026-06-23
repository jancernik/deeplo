package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jancernik/deeplo/internal/state"
)

func newDeploysHistoryCmd() *cobra.Command {
	var limit int
	var project string
	var host string

	cmd := &cobra.Command{
		Use:   "history",
		Short: "List recent deploy runs",
		Long:  `history lists recent deploy runs.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := daemonClient().Runs(cmd.Context(), project, host, limit)
			if err != nil {
				return err
			}
			return printHistoryTable(cmd, resp.Runs)
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "maximum number of runs to show")
	cmd.Flags().StringVar(&project, "project", "", "filter by project name")
	cmd.Flags().StringVar(&host, "host", "", "filter by host name")
	return cmd
}

func printHistoryTable(cmd *cobra.Command, runs []*state.Deployment) error {
	out := cmd.OutOrStdout()
	if len(runs) == 0 {
		fmt.Fprintln(out, "No runs recorded yet.") //nolint:errcheck
		return nil
	}

	writer := newTabWriter(out)
	fmt.Fprintln(writer, "ID\tPROJECT\tHOST\tSTATUS\tCOMMIT\tTRIGGER\tWHEN") //nolint:errcheck
	for _, run := range runs {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
			run.ID, run.Project, run.Host, formatStatus(run.Status),
			shortCommit(run.CommitSha), run.TriggerSource,
			formatRelativeTime(run.StartedAt),
		)
	}
	return writer.Flush()
}
