package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func ServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage the deeplo systemd service",
		Long: `Convenience commands for managing the deeplo systemd service.

These commands wrap systemctl and journalctl for the deeplo unit.`,
	}

	cmd.AddCommand(
		newServiceSubCmd("status", "Show the deeplo service status",
			func() error { return runSystemctl("status", nativeUnitName) }),
		newServiceSubCmd("start", "Start the deeplo service",
			func() error { return runSystemctlPrivileged("start", nativeUnitName) }),
		newServiceSubCmd("stop", "Stop the deeplo service",
			func() error { return runSystemctlPrivileged("stop", nativeUnitName) }),
		newServiceSubCmd("restart", "Restart the deeplo service",
			func() error { return runSystemctlPrivileged("restart", nativeUnitName) }),
		newServiceEnableCmd(),
		newServiceSubCmd("disable", "Disable the deeplo service from starting on boot",
			func() error { return runSystemctlPrivileged("disable", nativeUnitName) }),
		newServiceLogsCmd(),
	)

	return cmd
}

func newServiceSubCmd(use, short string, action func() error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNative(); err != nil {
				return err
			}
			return action()
		},
	}
}

func newServiceEnableCmd() *cobra.Command {
	var now bool

	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Enable the deeplo service to start on boot",
		Long: `Enable the deeplo service to start on boot.

Use --now to also start the service immediately.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNative(); err != nil {
				return err
			}
			systemctlArgs := []string{"enable"}
			if now {
				systemctlArgs = append(systemctlArgs, "--now")
			}
			systemctlArgs = append(systemctlArgs, nativeUnitName)
			return runSystemctlPrivileged(systemctlArgs...)
		},
	}

	cmd.Flags().BoolVar(&now, "now", false, "also start the service immediately")
	return cmd
}

func newServiceLogsCmd() *cobra.Command {
	var follow bool
	var lines int

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show deeplo service logs",
		Long: `Show logs for the deeplo systemd service via journalctl.

Use --follow (-f) to stream live output.
Use --lines (-n) to control how many recent lines are shown before streaming begins.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNative(); err != nil {
				return err
			}
			jArgs := []string{"-u", nativeUnitName, "--no-pager"}
			if follow {
				jArgs = append(jArgs, "-f")
			}
			if lines > 0 {
				jArgs = append(jArgs, fmt.Sprintf("-n%d", lines))
			}
			return runJournalctl(jArgs...)
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream live log output")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "number of recent lines to show")
	return cmd
}
