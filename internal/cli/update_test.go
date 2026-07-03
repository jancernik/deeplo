package cli

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jancernik/deeplo/internal/build"
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

// TestParseLatestRelease verifies the GitHub release response parser against
// compact single-line JSON (the live API format), pretty-printed JSON, and
// responses without a tag.
func TestParseLatestRelease(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "compact single-line JSON with url field first",
			body: `{"url":"https://api.github.com/repos/jancernik/deeplo/releases/348344382","tag_name":"v0.1.0","name":"v0.1.0"}`,
			want: "v0.1.0",
		},
		{
			name: "pretty-printed JSON",
			body: "{\n  \"url\": \"https://api.github.com/repos/jancernik/deeplo/releases/1\",\n  \"tag_name\": \"v1.2.3\"\n}",
			want: "v1.2.3",
		},
		{
			name:    "missing tag_name",
			body:    `{"message":"Not Found"}`,
			wantErr: true,
		},
		{
			name:    "not JSON",
			body:    "<html>rate limited</html>",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLatestRelease(strings.NewReader(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got tag %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("tag = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUpdateSkipsWhenAlreadyInstalled verifies that update is a no-op when the
// target version matches the running binary, and that --force overrides it.
func TestUpdateSkipsWhenAlreadyInstalled(t *testing.T) {
	overrideNative(t, nil)
	withAdminSocket(t, "/nonexistent.sock")

	origVersion := build.Version
	t.Cleanup(func() { build.Version = origVersion })
	build.Version = "1.2.3"

	origFetch := fetchBinary
	t.Cleanup(func() { fetchBinary = origFetch })
	var fetched bool
	fetchBinary = func(io.Writer, string, string) error {
		fetched = true
		return errors.New("stop here")
	}

	var out strings.Builder
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(&out)
	root.AddCommand(UpdateCmd())
	root.SetArgs([]string{"update", "--version", "v1.2.3"})
	if err := root.Execute(); err != nil {
		t.Fatalf("update on current version: expected clean exit, got: %v", err)
	}
	if fetched {
		t.Error("update on current version: fetchBinary should not be called")
	}
	if !strings.Contains(out.String(), "already installed") {
		t.Errorf("expected the already-installed notice, got: %q", out.String())
	}

	// --force proceeds to the download.
	root = &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(UpdateCmd())
	root.SetArgs([]string{"update", "--version", "v1.2.3", "--force"})
	_ = root.Execute()
	if !fetched {
		t.Error("update --force: fetchBinary should be called")
	}
}
