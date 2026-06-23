package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// executeEnvCmd runs a command under a minimal root with the env command group
// attached and returns stdout, stderr, and any error.
func executeEnvCmd(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(EnvCmd())
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// TestEnvPathRequiresNative verifies that "env path" fails on non-native installs.
func TestEnvPathRequiresNative(t *testing.T) {
	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return errors.New("not native") }

	_, _, err := executeEnvCmd(t, []string{"env", "path"})
	if err == nil {
		t.Fatal("env path: expected error on non-native install, got nil")
	}
}

// TestEnvEditRequiresNative verifies that "env edit" fails on non-native installs.
func TestEnvEditRequiresNative(t *testing.T) {
	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return errors.New("not native") }

	_, _, err := executeEnvCmd(t, []string{"env", "edit"})
	if err == nil {
		t.Fatal("env edit: expected error on non-native install, got nil")
	}
}

// TestEnvPathNative verifies that "env path" prints nativeEnvFile on native installs.
func TestEnvPathNative(t *testing.T) {
	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return nil }

	stdout, _, err := executeEnvCmd(t, []string{"env", "path"})
	if err != nil {
		t.Fatalf("env path: unexpected error: %v", err)
	}
	if !strings.Contains(stdout, nativeEnvFile) {
		t.Errorf("env path: expected %q in output, got %q", nativeEnvFile, stdout)
	}
}

// "env edit" announces sudoedit, invokes runSudoEdit with the env file path,
// and prints "Edited" plus the restart reminder.
func TestEnvEditCallsSudoedit(t *testing.T) {
	tmp := t.TempDir()
	fakeEnv := filepath.Join(tmp, "deeplo.env")
	if err := os.WriteFile(fakeEnv, []byte("KEY=val\n"), 0644); err != nil {
		t.Fatal(err)
	}

	origEnvFile := nativeEnvFile
	t.Cleanup(func() { nativeEnvFile = origEnvFile })
	nativeEnvFile = fakeEnv

	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return nil }

	origSudoEdit := runSudoEdit
	t.Cleanup(func() { runSudoEdit = origSudoEdit })
	var editedPath string
	runSudoEdit = func(path string) error {
		editedPath = path
		return nil
	}

	stdout, _, err := executeEnvCmd(t, []string{"env", "edit"})
	if err != nil {
		t.Fatalf("env edit: unexpected error: %v", err)
	}
	if editedPath != fakeEnv {
		t.Errorf("env edit: runSudoEdit called with %q, want %q", editedPath, fakeEnv)
	}
	for _, want := range []string{"Opening", "sudoedit", "Edited", fakeEnv, "deeplo service restart"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("env edit: output missing %q, got:\n%s", want, stdout)
		}
	}
}

// TestEnvEditFileNotFound verifies that "env edit" fails clearly when the env
// file does not exist (native install assumed).
func TestEnvEditFileNotFound(t *testing.T) {
	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return nil }

	// nativeEnvFile won't exist in the test environment
	_, _, err := executeEnvCmd(t, []string{"env", "edit"})
	if err == nil {
		t.Fatal("env edit: expected error for missing env file, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("env edit: error should mention 'not found', got %q", err)
	}
}

// TestServiceCanReadFileReadable exercises the real serviceCanReadFile: a file
// the current user can read returns (true, nil) without touching sudo.
func TestServiceCanReadFileReadable(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "deploy_key")
	if err := os.WriteFile(keyFile, []byte("KEY"), 0600); err != nil {
		t.Fatal(err)
	}
	readable, err := serviceCanReadFile(keyFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !readable {
		t.Error("expected a readable file to report readable")
	}
}

// TestServiceCanReadFileMissing exercises the real serviceCanReadFile: a missing
// file surfaces the underlying error rather than a false "unreadable".
func TestServiceCanReadFileMissing(t *testing.T) {
	readable, err := serviceCanReadFile(filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if readable {
		t.Error("a missing file must not report readable")
	}
}
