package cli

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// executeConfigCmd runs a command under a minimal root with the config command
// group attached and returns stdout, stderr, and any error.
func executeConfigCmd(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(ConfigCmd())
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// TestConfigPath verifies that "config path" prints the path from DEEPLO_CONFIG_FILE.
func TestConfigPath(t *testing.T) {
	t.Setenv("DEEPLO_CONFIG_FILE", "/tmp/test-config.yml")
	stdout, _, err := executeConfigCmd(t, []string{"config", "path"})
	if err != nil {
		t.Fatalf("config path: unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "/tmp/test-config.yml") {
		t.Errorf("config path: expected path in output, got %q", stdout)
	}
}

// TestConfigEditFileNotFound verifies that "config edit" fails clearly when the
// config file does not exist.
func TestConfigEditFileNotFound(t *testing.T) {
	t.Setenv("DEEPLO_CONFIG_FILE", "/nonexistent/path/config.yml")
	_, _, err := executeConfigCmd(t, []string{"config", "edit"})
	if err == nil {
		t.Fatal("config edit: expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("config edit: error should mention 'not found', got %q", err)
	}
}

// TestConfigEditCallsEditor verifies that "config edit" announces the editor,
// calls runEditor with the correct path, and prints "Edited" + next steps.
func TestConfigEditCallsEditor(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(f, []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPLO_CONFIG_FILE", f)
	overrideIsWritable(t, true)

	var editedPath string
	orig := runEditor
	t.Cleanup(func() { runEditor = orig })
	runEditor = func(path string) error {
		editedPath = path
		return nil
	}

	stdout, _, err := executeConfigCmd(t, []string{"config", "edit"})
	if err != nil {
		t.Fatalf("config edit: unexpected error: %v", err)
	}
	if editedPath != f {
		t.Errorf("config edit: editor called with %q, want %q", editedPath, f)
	}
	for _, want := range []string{"Opening", f, "Edited", "deeplo config check", "deeplo config reload"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("config edit: output missing %q, got:\n%s", want, stdout)
		}
	}
}

// TestConfigEditEditorError verifies that a non-zero editor exit is propagated.
func TestConfigEditEditorError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(f, []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPLO_CONFIG_FILE", f)

	orig := runEditor
	t.Cleanup(func() { runEditor = orig })
	runEditor = func(path string) error { return errors.New("editor failed") }

	_, _, err := executeConfigCmd(t, []string{"config", "edit"})
	if err == nil {
		t.Fatal("config edit: expected error from editor failure, got nil")
	}
}

// TestConfigReloadHitsDaemon verifies "config reload" posts to the daemon's
// reload endpoint and prints the returned message.
func TestConfigReloadHitsDaemon(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/reload", serveJSON(map[string]any{"message": "config reloaded"}))
	sock := startFakeDaemon(t, mux)

	withAdminSocket(t, sock)
	out, _, err := runCmd(t, ConfigCmd(), []string{"config", "reload"})
	if err != nil {
		t.Fatalf("config reload: unexpected error: %v", err)
	}
	if !strings.Contains(out, "config reloaded") {
		t.Errorf("config reload: expected daemon message, got:\n%s", out)
	}
}

// TestConfigReloadRequiresDaemon verifies "config reload" errors when the daemon
// is unreachable.
func TestConfigReloadRequiresDaemon(t *testing.T) {
	withAdminSocket(t, filepath.Join(t.TempDir(), "no.sock"))
	if _, _, err := runCmd(t, ConfigCmd(), []string{"config", "reload"}); err == nil {
		t.Fatal("config reload: expected error when daemon unreachable")
	}
}

// overrideIsWritable replaces isWritableFile for the duration of the test.
func overrideIsWritable(t *testing.T, writable bool) {
	t.Helper()
	orig := isWritableFile
	t.Cleanup(func() { isWritableFile = orig })
	isWritableFile = func(string) bool { return writable }
}

// "config edit" falls back to sudoedit when the config file isn't writable by
// the current user (e.g. root-owned), announcing sudoedit and printing "Edited".
func TestConfigEditUsesSudoeditWhenNotWritable(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(f, []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPLO_CONFIG_FILE", f)

	overrideIsWritable(t, false)

	var sudoEditPath string
	origSudoEdit := runSudoEdit
	t.Cleanup(func() { runSudoEdit = origSudoEdit })
	runSudoEdit = func(path string) error {
		sudoEditPath = path
		return nil
	}

	var editorCalled bool
	origEditor := runEditor
	t.Cleanup(func() { runEditor = origEditor })
	runEditor = func(path string) error {
		editorCalled = true
		return nil
	}

	stdout, _, err := executeConfigCmd(t, []string{"config", "edit"})
	if err != nil {
		t.Fatalf("config edit (not writable): unexpected error: %v", err)
	}
	if sudoEditPath != f {
		t.Errorf("config edit (not writable): runSudoEdit called with %q, want %q", sudoEditPath, f)
	}
	if editorCalled {
		t.Error("config edit (not writable): runEditor should not be called for non-writable file")
	}
	for _, want := range []string{"Opening", "sudoedit", "Edited", f, "deeplo config check"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("config edit (not writable): output missing %q, got:\n%s", want, stdout)
		}
	}
}

// TestConfigEditUsesEditorWhenWritable verifies that "config edit" calls the
// normal editor (not sudoedit) when the file is writable by the current user.
func TestConfigEditUsesEditorWhenWritable(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(f, []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPLO_CONFIG_FILE", f)

	overrideIsWritable(t, true)

	var editorPath string
	origEditor := runEditor
	t.Cleanup(func() { runEditor = origEditor })
	runEditor = func(path string) error {
		editorPath = path
		return nil
	}

	var sudoEditCalled bool
	origSudoEdit := runSudoEdit
	t.Cleanup(func() { runSudoEdit = origSudoEdit })
	runSudoEdit = func(path string) error {
		sudoEditCalled = true
		return nil
	}

	_, _, err := executeConfigCmd(t, []string{"config", "edit"})
	if err != nil {
		t.Fatalf("config edit (writable): unexpected error: %v", err)
	}
	if editorPath != f {
		t.Errorf("config edit (writable): runEditor called with %q, want %q", editorPath, f)
	}
	if sudoEditCalled {
		t.Error("config edit (writable): runSudoEdit should not be called for writable file")
	}
}
