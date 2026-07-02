package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jancernik/deeplo/internal/api"
)

func ProbeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "probe",
		Short: "Check SSH connectivity to every host",
		Long: `Ask the running daemon to dial each configured host over SSH with the
deploy key and report connectivity.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := daemonClient().Probe(cmd.Context())
			if err != nil {
				return err
			}
			return printProbeResults(cmd, resp.Hosts)
		},
	}
}

func printProbeResults(cmd *cobra.Command, hosts []api.ProbeHost) error {
	out := cmd.OutOrStdout()
	if len(hosts) == 0 {
		fmt.Fprintln(out, "No hosts configured.") //nolint:errcheck
		return nil
	}

	var failed int
	for _, host := range hosts {
		if host.OK {
			fmt.Fprintf(out, "OK    %-20s  %s\n", host.Host, host.Address) //nolint:errcheck
		} else {
			fmt.Fprintf(out, "FAIL  %-20s  %s\n", host.Host, host.Error) //nolint:errcheck
			failed++
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d host(s) unreachable", failed)
	}
	return nil
}
