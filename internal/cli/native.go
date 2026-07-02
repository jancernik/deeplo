package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"time"

	"github.com/jancernik/deeplo/internal/client"
)

const nativeUnitName = "deeplo"
const nativeServiceUser = "deeplo"

// Vars so tests can redirect them to temp paths without requiring root access.
var (
	nativeUnitFile = "/etc/systemd/system/deeplo.service"
	nativeEnvFile  = "/etc/deeplo/deeplo.env"
)

var errNotNative = fmt.Errorf("this command requires a native install (systemd)")

// Checks whether systemd appears to be the active init system on this host.
var checkNativeInstall = func() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errNotNative
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errNotNative
	}
	return nil
}

func requireNative() error { return checkNativeInstall() }

var runSystemctl = func(args ...string) error {
	c := exec.Command("systemctl", args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// Executes systemctl for mutating operations start, stop, restart, enable, disable).
// When not running as root it prepends sudo.
var runSystemctlPrivileged = func(args ...string) error {
	var c *exec.Cmd
	if os.Getuid() == 0 {
		c = exec.Command("systemctl", args...)
	} else {
		if _, err := exec.LookPath("sudo"); err != nil {
			return fmt.Errorf("root privileges required; sudo not found in PATH\n  re-run as root or install sudo")
		}
		c = exec.Command("sudo", append([]string{"systemctl"}, args...)...)
	}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin // needed so sudo can prompt for a password
	return c.Run()
}

// Reports whether the current user can read the system journal without escalation.
var canReadSystemJournal = func() bool {
	if os.Getuid() == 0 {
		return true
	}
	groupIDs, err := os.Getgroups()
	if err != nil {
		return false
	}
	for _, groupName := range []string{"systemd-journal", "adm"} {
		group, lookupErr := user.LookupGroup(groupName)
		if lookupErr != nil {
			continue
		}
		for _, groupID := range groupIDs {
			if strconv.Itoa(groupID) == group.Gid {
				return true
			}
		}
	}
	return false
}

// Executes journalctl with the given args, wiring stdio directly to the terminal.
var runJournalctl = func(args ...string) error {
	var c *exec.Cmd
	if canReadSystemJournal() {
		c = exec.Command("journalctl", args...)
	} else {
		if _, err := exec.LookPath("sudo"); err != nil {
			return fmt.Errorf("reading the system journal requires privileges; sudo not found in PATH\n  re-run as root, install sudo, or add your user to the 'systemd-journal' group")
		}
		c = exec.Command("sudo", append([]string{"journalctl"}, args...)...)
	}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin // needed so sudo can prompt for a password
	return c.Run()
}

func resolveEditor() string {
	if e := os.Getenv("VISUAL"); e != "" {
		return e
	}
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	return "vi"
}

var runEditor = func(path string) error {
	c := exec.Command(resolveEditor(), path)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}

var isWritableFile = func(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

var runSudoEdit = func(path string) error {
	c := exec.Command("sudo", "-e", path)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}

var systemctlExitZero = func(args ...string) bool {
	return exec.Command("systemctl", args...).Run() == nil
}

// Attempts a Health call against the daemon at socket and returns true if it responds within 2 seconds.
var daemonReachable = func(socket string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := client.New(socket).Health(ctx)
	return err == nil
}
