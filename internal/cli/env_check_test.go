package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runEnvCheckCmd runs `env check` against an env file at envPath with native
// detection forced on, returning the combined output and the command error.
func runEnvCheckCmd(t *testing.T, envPath string) (string, error) {
	t.Helper()
	overrideNative(t, nil)

	origEnvFile := nativeEnvFile
	t.Cleanup(func() { nativeEnvFile = origEnvFile })
	nativeEnvFile = envPath

	var out strings.Builder
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(&out)
	root.SetErr(&out)
	root.AddCommand(EnvCmd())
	root.SetArgs([]string{"env", "check"})
	err := root.Execute()
	return out.String(), err
}

func TestEnvCheckValid(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte("KEY"), 0600); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(dir, "deeplo.env")
	contents := "DEEPLO_DATA_DIR=" + dir + "\nDEEPLO_SSH_PRIVATE_KEY_FILE=" + keyFile + "\n"
	if err := os.WriteFile(envFile, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	out, err := runEnvCheckCmd(t, envFile)
	if err != nil {
		t.Fatalf("env check: valid env should pass, got err: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Env OK") {
		t.Errorf("env check: expected 'Env OK', got:\n%s", out)
	}
}

func TestEnvCheckMissingRequired(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "deeplo.env")
	// Missing DEEPLO_SSH_PRIVATE_KEY_FILE and DEEPLO_DATA_DIR.
	if err := os.WriteFile(envFile, []byte("DEEPLO_LOG_LEVEL=debug\n"), 0600); err != nil {
		t.Fatal(err)
	}

	out, err := runEnvCheckCmd(t, envFile)
	if err == nil {
		t.Fatalf("env check: missing required vars should fail, output:\n%s", out)
	}
	if !strings.Contains(out, "ERROR") || !strings.Contains(out, "Env INVALID") {
		t.Errorf("env check: expected ERROR + INVALID, got:\n%s", out)
	}
}

func TestEnvCheckReferencedFileMissing(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "deeplo.env")
	contents := "DEEPLO_DATA_DIR=" + dir + "\nDEEPLO_SSH_PRIVATE_KEY_FILE=" + dir + "/does-not-exist\n"
	if err := os.WriteFile(envFile, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	out, err := runEnvCheckCmd(t, envFile)
	if err == nil {
		t.Fatalf("env check: missing key file should fail, output:\n%s", out)
	}
	if !strings.Contains(out, "DEEPLO_SSH_PRIVATE_KEY_FILE") {
		t.Errorf("env check: expected the missing key file to be reported, got:\n%s", out)
	}
}

func TestEnvCheckFileNotFound(t *testing.T) {
	_, err := runEnvCheckCmd(t, filepath.Join(t.TempDir(), "absent.env"))
	if err == nil {
		t.Fatal("env check: expected error when the env file is absent")
	}
}

// TestEnvCheckKeyReadableByServiceUserOnly verifies that a key the operator
// cannot read but the daemon's service user can is reported as OK - the check is
// made against the service user, not whoever runs the command.
func TestEnvCheckKeyReadableByServiceUserOnly(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "deploy_key")
	if err := os.WriteFile(keyFile, []byte("KEY"), 0600); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(dir, "deeplo.env")
	contents := "DEEPLO_DATA_DIR=" + dir + "\nDEEPLO_SSH_PRIVATE_KEY_FILE=" + keyFile + "\n"
	if err := os.WriteFile(envFile, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	orig := serviceCanReadFile
	t.Cleanup(func() { serviceCanReadFile = orig })
	serviceCanReadFile = func(string) (bool, error) { return true, nil }

	out, err := runEnvCheckCmd(t, envFile)
	if err != nil {
		t.Fatalf("env check: service-readable key should pass, got err: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Env OK") {
		t.Errorf("env check: expected 'Env OK', got:\n%s", out)
	}
}

// TestEnvCheckKeyUnreadableByServiceUser verifies that a key the daemon's
// service user cannot read is reported as an error, naming that user.
func TestEnvCheckKeyUnreadableByServiceUser(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "deploy_key")
	if err := os.WriteFile(keyFile, []byte("KEY"), 0600); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(dir, "deeplo.env")
	contents := "DEEPLO_DATA_DIR=" + dir + "\nDEEPLO_SSH_PRIVATE_KEY_FILE=" + keyFile + "\n"
	if err := os.WriteFile(envFile, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	orig := serviceCanReadFile
	t.Cleanup(func() { serviceCanReadFile = orig })
	serviceCanReadFile = func(string) (bool, error) { return false, nil }

	out, err := runEnvCheckCmd(t, envFile)
	if err == nil {
		t.Fatalf("env check: unreadable key should fail, output:\n%s", out)
	}
	if !strings.Contains(out, "DEEPLO_SSH_PRIVATE_KEY_FILE") || !strings.Contains(out, nativeServiceUser) {
		t.Errorf("env check: expected the key field and service user named, got:\n%s", out)
	}
}
