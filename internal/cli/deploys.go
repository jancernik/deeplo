package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jancernik/deeplo/internal/state"
)

func DeploysCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "deploys",
		Aliases: []string{"deployments"},
		Short:   "Inspect deployments: state, containers, history, and logs",
	}
	cmd.AddCommand(
		newDeploysStateCmd(),
		newDeploysContainersCmd(),
		newDeploysHistoryCmd(),
		newDeploysLogsCmd(),
	)
	return cmd
}

func newDeploysStateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "state",
		Short: "Show the latest recorded deployment per project and host",
		Long:  `state shows the latest recorded deployment state per project-host pair.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := daemonClient().Deployments(cmd.Context())
			if err != nil {
				return err
			}
			return printDeploysStateTable(cmd, resp.Deployments)
		},
	}
}

func newDeploysContainersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "containers",
		Short: "SSH into each host and show observed container state",
		Long: `containers asks the daemon to SSH into every configured host and run
'docker compose ps', printing the observed runtime state.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploysContainers(cmd)
		},
	}
}

func printDeploysStateTable(cmd *cobra.Command, deployments []*state.Deployment) error {
	out := cmd.OutOrStdout()
	if len(deployments) == 0 {
		fmt.Fprintln(out, "No deployments recorded yet.")                                        //nolint:errcheck
		fmt.Fprintln(out, "Trigger one by pushing to a tracked branch, or wait for the poller.") //nolint:errcheck
		return nil
	}

	hasReporting := false
	for _, deployment := range deployments {
		if deployment.ReportToken != "" || deployment.ReportStatus != "" {
			hasReporting = true
			break
		}
	}

	writer := newTabWriter(out)
	if hasReporting {
		fmt.Fprintln(writer, "PROJECT\tHOST\tSTATUS\tCOMMIT\tTRIGGER\tREPORT\tWHEN") //nolint:errcheck
	} else {
		fmt.Fprintln(writer, "PROJECT\tHOST\tSTATUS\tCOMMIT\tTRIGGER\tWHEN") //nolint:errcheck
	}
	for _, deployment := range deployments {
		if hasReporting {
			reportStatus := deployment.ReportStatus
			if reportStatus == "" && deployment.ReportError != "" {
				reportStatus = "error"
			}
			if reportStatus == "" {
				reportStatus = "-"
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
				deployment.Project, deployment.Host, formatStatus(deployment.Status),
				shortCommit(deployment.CommitSha), deployment.TriggerSource,
				reportStatus, formatRelativeTime(deployment.StartedAt),
			)
		} else {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
				deployment.Project, deployment.Host, formatStatus(deployment.Status),
				shortCommit(deployment.CommitSha), deployment.TriggerSource,
				formatRelativeTime(deployment.StartedAt),
			)
		}
	}
	return writer.Flush()
}

func runDeploysContainers(cmd *cobra.Command) error {
	resp, err := daemonClient().Refresh(cmd.Context())
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if len(resp.Hosts) == 0 {
		fmt.Fprintln(out, "No hosts configured.") //nolint:errcheck
		return nil
	}

	writer := newTabWriter(out)
	fmt.Fprintln(writer, "PROJECT\tHOST\tSERVICE\tSTATE\tSTATUS") //nolint:errcheck

	var anyError bool
	for _, host := range resp.Hosts {
		if host.Error != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "ERROR  %s: %s\n", host.Host, host.Error) //nolint:errcheck
			anyError = true
			continue
		}
		for _, project := range host.Projects {
			if project.Error != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "ERROR  %s/%s: %s\n", project.Project, host.Host, project.Error) //nolint:errcheck
				anyError = true
				continue
			}
			if len(project.Services) == 0 {
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
					project.Project, host.Host, "(none)", "-", "not deployed")
				continue
			}
			for _, service := range project.Services {
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
					project.Project, host.Host, service.Service, service.State, service.Status)
			}
		}
	}
	_ = writer.Flush()

	if anyError {
		return fmt.Errorf("one or more hosts could not be reached")
	}
	return nil
}

func formatStatus(status state.DeploymentStatus) string {
	switch status {
	case state.StatusSuccess:
		return "success"
	case state.StatusFailed:
		return "failed"
	case state.StatusRunning:
		return "running"
	case state.StatusPending:
		return "pending"
	default:
		return string(status)
	}
}
