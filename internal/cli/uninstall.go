package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func UninstallCmd() *cobra.Command {
	var purge bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall deeplo and its systemd service from this host",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNative(); err != nil {
				return err
			}
			return runUninstall(cmd, purge)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "purge config, data, and the system user/group without prompting")
	return cmd
}

func runUninstall(cmd *cobra.Command, purge bool) error {
	out := cmd.OutOrStdout()

	if !purge {
		fmt.Fprintln(out, "This will stop and remove the deeplo service and binary.")      //nolint:errcheck
		fmt.Fprint(out, "Also remove config, data, and the deeplo user and group? [y/N] ") //nolint:errcheck
		reader := bufio.NewReader(cmd.InOrStdin())
		answer, _ := reader.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a == "y" || a == "yes" {
			purge = true
		}
	}

	fmt.Fprintln(out, "Uninstalling deeplo...") //nolint:errcheck

	// Stop and disable the service.
	if systemctlExitZero("is-active", "--quiet", nativeUnitName) {
		if err := runSystemctlPrivileged("stop", nativeUnitName); err != nil {
			return fmt.Errorf("stop %s: %w", nativeUnitName, err)
		}
		fmt.Fprintf(out, "✓ Stopped %s\n", nativeUnitName) //nolint:errcheck
	}
	if systemctlExitZero("is-enabled", "--quiet", nativeUnitName) {
		_ = runSystemctlPrivileged("disable", nativeUnitName)
	}
	if systemctlExitZero("is-failed", "--quiet", nativeUnitName) {
		_ = runSystemctlPrivileged("reset-failed", nativeUnitName)
	}

	// Remove the unit file.
	unitFile := nativeUnitFile
	if _, err := os.Stat(unitFile); err == nil {
		if err := removeFilePrivileged(unitFile); err != nil {
			return fmt.Errorf("remove %s: %w", unitFile, err)
		}
		fmt.Fprintf(out, "✓ Removed %s\n", unitFile) //nolint:errcheck
		_ = runSystemctlPrivileged("daemon-reload")
	}

	// Remove the binary.
	binPath := installDir + "/deeplo"
	if _, err := os.Stat(binPath); err == nil {
		if err := removeFilePrivileged(binPath); err != nil {
			return fmt.Errorf("remove %s: %w", binPath, err)
		}
		fmt.Fprintf(out, "✓ Removed %s\n", binPath) //nolint:errcheck
	}

	// Remove shell completions.
	for _, completion := range shellCompletions {
		if _, err := os.Stat(completion.file); err == nil {
			if err := removeFilePrivileged(completion.file); err != nil {
				return fmt.Errorf("remove %s: %w", completion.file, err)
			}
			fmt.Fprintf(out, "✓ Removed %s\n", completion.file) //nolint:errcheck
		}
	}

	if purge {
		for _, dir := range []string{"/etc/deeplo", "/var/lib/deeplo"} {
			if err := removeDirPrivileged(dir); err != nil {
				return fmt.Errorf("remove %s: %w", dir, err)
			}
			fmt.Fprintf(out, "✓ Removed %s\n", dir) //nolint:errcheck
		}
		// Remove the user. userdel also removes the user's primary group on most systems,
		// groupdel afterward is a silent best-effort fallback for systems that leave it behind.
		if err := removeSystemUser("deeplo"); err == nil {
			fmt.Fprintln(out, "✓ Removed system user and group deeplo") //nolint:errcheck
		}
		_ = removeSystemGroup("deeplo")
		fmt.Fprintln(out, "\ndeeplo fully purged.")                                                    //nolint:errcheck
		fmt.Fprintln(out, "\nNote: deployed apps and the authorized deploy key on your target")        //nolint:errcheck
		fmt.Fprintln(out, "hosts are not touched by uninstall - remove those on each host if needed.") //nolint:errcheck
	} else {
		fmt.Fprintln(out, "\ndeeplo uninstalled. Config and data preserved:")                   //nolint:errcheck
		fmt.Fprintln(out, "/etc/deeplo  /var/lib/deeplo")                                       //nolint:errcheck
		fmt.Fprintln(out, "\nRemove these later with: sudo rm -rf /etc/deeplo /var/lib/deeplo") //nolint:errcheck
		fmt.Fprintln(out, "(the deeplo system user and group also remain)")                     //nolint:errcheck
	}
	return nil
}

var removeFilePrivileged = func(path string) error {
	var c *exec.Cmd
	if os.Getuid() == 0 {
		c = exec.Command("rm", "-f", path)
	} else {
		c = exec.Command("sudo", "rm", "-f", path)
	}
	c.Stderr = os.Stderr
	return c.Run()
}

var removeDirPrivileged = func(path string) error {
	var c *exec.Cmd
	if os.Getuid() == 0 {
		c = exec.Command("rm", "-rf", path)
	} else {
		c = exec.Command("sudo", "rm", "-rf", path)
	}
	c.Stderr = os.Stderr
	return c.Run()
}

var removeSystemUser = func(user string) error {
	var c *exec.Cmd
	if os.Getuid() == 0 {
		c = exec.Command("userdel", user)
	} else {
		c = exec.Command("sudo", "userdel", user)
	}
	c.Stderr = os.Stderr
	return c.Run()
}

var removeSystemGroup = func(group string) error {
	if os.Getuid() == 0 {
		return exec.Command("groupdel", group).Run()
	}
	return exec.Command("sudo", "groupdel", group).Run()
}
