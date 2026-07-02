package cli

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestUpdateRequiresNative verifies that "deeplo update" fails on non-native installs.
func TestUpdateRequiresNative(t *testing.T) {
	nativeErr := errors.New("not a native install")
	_, err := runManageCmd(t, []string{"update"}, nativeErr)
	if err == nil {
		t.Fatal("update: expected error on non-native install, got nil")
	}
}

// TestUpdateDockerUnsupportedMessage verifies the error text mentions the restriction.
func TestUpdateDockerUnsupportedMessage(t *testing.T) {
	nativeErr := errors.New("not a native install: systemctl not found")
	_, err := runManageCmd(t, []string{"update"}, nativeErr)
	if err == nil {
		t.Fatal("update: expected error on non-native install, got nil")
	}
	// errNotNative is returned by requireNative via checkNativeInstall.
	// The error should mention this is a systemd-only command.
	if !strings.Contains(errNotNative.Error(), "systemd") {
		t.Errorf("update: errNotNative should mention systemd, got: %q", errNotNative.Error())
	}
}

// TestUpdateVersionFlag verifies that "deeplo update --version v1.2.3" passes
// the version to the binary fetch without querying GitHub (fetchLatestVersion
// should not be called).
func TestUpdateVersionFlag(t *testing.T) {
	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return nil }

	origLatest := fetchLatestVersion
	t.Cleanup(func() { fetchLatestVersion = origLatest })
	fetchLatestVersion = func() (string, error) {
		t.Error("update --version: fetchLatestVersion should not be called when version is explicit")
		return "", errors.New("should not be called")
	}

	origFetch := fetchBinary
	t.Cleanup(func() { fetchBinary = origFetch })
	var fetchedVersion string
	fetchBinary = func(_ io.Writer, version, dst string) error {
		fetchedVersion = version
		// Return an error to abort early - we only care that version was passed correctly.
		return errors.New("stop here")
	}

	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	withAdminSocket(t, "/nonexistent.sock")
	root.AddCommand(UpdateCmd())
	root.SetArgs([]string{"update", "--version", "v1.2.3"})
	_ = root.Execute()

	if fetchedVersion != "v1.2.3" {
		t.Errorf("update --version: fetchBinary called with %q, want %q", fetchedVersion, "v1.2.3")
	}
}

// TestUpdateRefreshesCompletions verifies that a successful update regenerates
// shell completions with the freshly installed binary.
func TestUpdateRefreshesCompletions(t *testing.T) {
	overrideNative(t, nil)

	origFetch := fetchBinary
	t.Cleanup(func() { fetchBinary = origFetch })
	fetchBinary = func(io.Writer, string, string) error { return nil }

	origInstall := installBinaryTo
	t.Cleanup(func() { installBinaryTo = origInstall })
	installBinaryTo = func(string, string) error { return nil }

	origPriv := runSystemctlPrivileged
	t.Cleanup(func() { runSystemctlPrivileged = origPriv })
	runSystemctlPrivileged = func(...string) error { return nil }

	dir := t.TempDir()
	overrideShellCompletions(t, []shellCompletion{
		{shell: "bash", dir: dir, file: filepath.Join(dir, "deeplo")},
	})

	var refreshedWithBinary string
	stubGenerateCompletion(t, func(binPath, _ string) ([]byte, error) {
		refreshedWithBinary = binPath
		return []byte("script"), nil
	})
	stubInstallCompletionFile(t, func([]byte, string) error { return nil })

	var out strings.Builder
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(&out)
	// Socket points nowhere, so the daemon reads as not running and no restart is attempted.
	withAdminSocket(t, "/nonexistent.sock")
	root.AddCommand(UpdateCmd())
	root.SetArgs([]string{"update", "--version", "v1.2.3"})
	if err := root.Execute(); err != nil {
		t.Fatalf("update: %v", err)
	}

	if refreshedWithBinary != filepath.Join(installDir, "deeplo") {
		t.Errorf("completions refreshed with %q, want the installed binary %q",
			refreshedWithBinary, filepath.Join(installDir, "deeplo"))
	}
	if !strings.Contains(out.String(), "Refreshed shell completions (bash)") {
		t.Errorf("update output missing completion refresh line, got: %q", out.String())
	}
}
