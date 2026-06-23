package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestUninstallRequiresNative verifies that "deeplo uninstall" fails on non-native installs.
func TestUninstallRequiresNative(t *testing.T) {
	nativeErr := errors.New("not a native install")
	_, err := runManageCmd(t, []string{"uninstall"}, nativeErr)
	if err == nil {
		t.Fatal("remove: expected error on non-native install, got nil")
	}
}

// TestUninstallDockerUnsupportedMessage verifies the error mentions the systemd restriction.
func TestUninstallDockerUnsupportedMessage(t *testing.T) {
	nativeErr := errors.New("not a native install")
	_, err := runManageCmd(t, []string{"uninstall"}, nativeErr)
	if err == nil {
		t.Fatal("remove: expected error on non-native install, got nil")
	}
	if !strings.Contains(errNotNative.Error(), "systemd") {
		t.Errorf("remove: errNotNative should mention systemd, got: %q", errNotNative.Error())
	}
}

// TestUninstallPurgeRemovesUserAndGroup verifies that --purge removes the data and
// config directories and BOTH the system user and group (the group is created
// by the installer and must not be orphaned).
func TestUninstallPurgeRemovesUserAndGroup(t *testing.T) {
	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return nil }

	origExitZero := systemctlExitZero
	t.Cleanup(func() { systemctlExitZero = origExitZero })
	systemctlExitZero = func(args ...string) bool { return false }

	origPriv := runSystemctlPrivileged
	t.Cleanup(func() { runSystemctlPrivileged = origPriv })
	runSystemctlPrivileged = func(args ...string) error { return nil }

	origDir := removeDirPrivileged
	t.Cleanup(func() { removeDirPrivileged = origDir })
	var removedDirs []string
	removeDirPrivileged = func(path string) error { removedDirs = append(removedDirs, path); return nil }

	origUser := removeSystemUser
	t.Cleanup(func() { removeSystemUser = origUser })
	var userRemoved bool
	removeSystemUser = func(string) error { userRemoved = true; return nil }

	origGroup := removeSystemGroup
	t.Cleanup(func() { removeSystemGroup = origGroup })
	var groupRemoved bool
	removeSystemGroup = func(string) error { groupRemoved = true; return nil }

	var outBuf strings.Builder
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(&outBuf)
	root.AddCommand(UninstallCmd())
	root.SetArgs([]string{"uninstall", "--purge"})
	if err := root.Execute(); err != nil {
		t.Fatalf("remove --purge: %v", err)
	}

	if !userRemoved {
		t.Error("expected system user to be removed on --purge")
	}
	if !groupRemoved {
		t.Error("expected system group to be removed on --purge")
	}
	if got := strings.Join(removedDirs, ","); got != "/etc/deeplo,/var/lib/deeplo" {
		t.Errorf("expected /etc/deeplo and /var/lib/deeplo uninstalld, got: %q", got)
	}
	if !strings.Contains(outBuf.String(), "Removed system user and group deeplo") {
		t.Errorf("expected user/group-removal message, got: %q", outBuf.String())
	}
}

// runInteractiveUninstall drives "deeplo uninstall" with the given stdin answer and
// returns the captured output plus whether a purge (group removal) happened.
func runInteractiveUninstall(t *testing.T, stdin string) (string, bool) {
	t.Helper()

	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return nil }

	origExitZero := systemctlExitZero
	t.Cleanup(func() { systemctlExitZero = origExitZero })
	systemctlExitZero = func(args ...string) bool { return false }

	origPriv := runSystemctlPrivileged
	t.Cleanup(func() { runSystemctlPrivileged = origPriv })
	runSystemctlPrivileged = func(args ...string) error { return nil }

	origDir := removeDirPrivileged
	t.Cleanup(func() { removeDirPrivileged = origDir })
	removeDirPrivileged = func(string) error { return nil }

	origUser := removeSystemUser
	t.Cleanup(func() { removeSystemUser = origUser })
	removeSystemUser = func(string) error { return nil }

	origGroup := removeSystemGroup
	t.Cleanup(func() { removeSystemGroup = origGroup })
	var purged bool
	removeSystemGroup = func(string) error { purged = true; return nil }

	var outBuf strings.Builder
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(&outBuf)
	root.SetIn(strings.NewReader(stdin))
	root.AddCommand(UninstallCmd())
	root.SetArgs([]string{"uninstall"})
	if err := root.Execute(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	return outBuf.String(), purged
}

// TestUninstallInteractivePurge verifies that answering "y" triggers a full purge.
func TestUninstallInteractivePurge(t *testing.T) {
	out, purged := runInteractiveUninstall(t, "y\n")
	if !purged {
		t.Error("answer 'y' should purge (remove the system group)")
	}
	if !strings.Contains(out, "fully purged") {
		t.Errorf("expected 'fully purged' output, got: %q", out)
	}
}

// TestUninstallInteractivePlain verifies that answering "n" removes the binary but
// preserves config and data.
func TestUninstallInteractivePlain(t *testing.T) {
	out, purged := runInteractiveUninstall(t, "n\n")
	if purged {
		t.Error("answer 'n' should not purge config/data/user/group")
	}
	if !strings.Contains(out, "Config and data preserved") {
		t.Errorf("expected 'Config and data preserved' output, got: %q", out)
	}
}

// TestUninstallInteractiveDefault verifies that an empty answer defaults to no
// purge (config and data preserved).
func TestUninstallInteractiveDefault(t *testing.T) {
	out, purged := runInteractiveUninstall(t, "\n")
	if purged {
		t.Error("empty answer should default to no purge")
	}
	if !strings.Contains(out, "Config and data preserved") {
		t.Errorf("expected 'Config and data preserved' output, got: %q", out)
	}
}
