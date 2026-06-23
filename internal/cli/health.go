package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"
)

func HealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Show deeplo service health",
		Long:  `health shows the health of the deeplo service.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkNativeInstall() == nil {
				return healthNative(cmd)
			}
			return healthSocket(cmd)
		},
	}
}

func healthNative(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	if !systemctlExitZero("is-active", "--quiet", nativeUnitName) {
		fmt.Fprintln(out, "deeplo is not running.") //nolint:errcheck
		fmt.Fprintln(out)                           //nolint:errcheck
		fmt.Fprintln(out, "Start it with:")         //nolint:errcheck
		fmt.Fprintln(out, "  deeplo service start") //nolint:errcheck
		return errSilentExit
	}

	enabled := systemctlExitZero("is-enabled", "--quiet", nativeUnitName)

	fmt.Fprintf(out, "Service:  running\n")                             //nolint:errcheck
	fmt.Fprintf(out, "Enabled:  %s\n", boolLabel(enabled, "yes", "no")) //nolint:errcheck

	_, sockErr := os.Stat(adminSocket())
	switch {
	case sockErr == nil:
		fmt.Fprintf(out, "Socket:   present\n") //nolint:errcheck
		version, uptime, ok := healthPingDaemon(cmd.Context())
		if ok {
			fmt.Fprintf(out, "Daemon:   reachable\n")   //nolint:errcheck
			fmt.Fprintf(out, "Version:  %s\n", version) //nolint:errcheck
			fmt.Fprintf(out, "Uptime:   %s\n", uptime)  //nolint:errcheck
		} else {
			fmt.Fprintf(out, "Daemon:   unreachable\n") //nolint:errcheck
		}
	case errors.Is(sockErr, fs.ErrPermission):
		fmt.Fprintf(out, "Socket:   no access\n")                                                  //nolint:errcheck
		fmt.Fprintf(out, "Daemon:   not checked (no socket access)\n")                             //nolint:errcheck
		fmt.Fprintln(out)                                                                          //nolint:errcheck
		fmt.Fprintln(out, "Your user can't reach the admin socket. Point DEEPLO_ADMIN_GROUP at a") //nolint:errcheck
		fmt.Fprintln(out, "group you belong to, then restart:")                                    //nolint:errcheck
		fmt.Fprintln(out, "  deeplo env edit            # set DEEPLO_ADMIN_GROUP=<your group>")    //nolint:errcheck
		fmt.Fprintln(out, "  deeplo service restart")                                              //nolint:errcheck
	default:
		fmt.Fprintf(out, "Socket:   missing\n")                      //nolint:errcheck
		fmt.Fprintf(out, "Daemon:   not checked (socket missing)\n") //nolint:errcheck
	}
	return nil
}

func healthSocket(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	version, uptime, ok := healthPingDaemon(cmd.Context())
	if !ok {
		errOut := cmd.ErrOrStderr()
		fmt.Fprintf(errOut, "deeplo daemon is not reachable.\n")  //nolint:errcheck
		fmt.Fprintf(errOut, "Socket:  %s\n\n", adminSocket())     //nolint:errcheck
		fmt.Fprintf(errOut, "Is the deeplo container running?\n") //nolint:errcheck
		fmt.Fprintf(errOut, "Check with: docker ps\n")            //nolint:errcheck
		return errSilentExit
	}
	fmt.Fprintf(out, "Daemon:   reachable\n")   //nolint:errcheck
	fmt.Fprintf(out, "Version:  %s\n", version) //nolint:errcheck
	fmt.Fprintf(out, "Uptime:   %s\n", uptime)  //nolint:errcheck
	return nil
}

var healthPingDaemon = func(ctx context.Context) (version, uptime string, ok bool) {
	resp, err := daemonClient().Health(ctx)
	if err != nil {
		return "", "", false
	}
	return resp.Version, resp.Uptime, true
}

func boolLabel(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
