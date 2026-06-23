package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadNativeEnvFile verifies DEEPLO_* vars are pulled from the env file,
// and that a value already set in the environment is left untouched.
func TestLoadNativeEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deeplo.env")
	contents := "DEEPLO_TEST_KEY=/from/file\nDEEPLO_TEST_PRESET=/from/file\n"
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}

	origEnvFile := nativeEnvFile
	t.Cleanup(func() { nativeEnvFile = origEnvFile })
	nativeEnvFile = path

	t.Setenv("DEEPLO_TEST_PRESET", "/from/env") // already set: must win
	t.Cleanup(func() { _ = os.Unsetenv("DEEPLO_TEST_KEY") })

	loadNativeEnvFile()

	if got := os.Getenv("DEEPLO_TEST_KEY"); got != "/from/file" {
		t.Errorf("DEEPLO_TEST_KEY = %q, want /from/file", got)
	}
	if got := os.Getenv("DEEPLO_TEST_PRESET"); got != "/from/env" {
		t.Errorf("preset var overridden: got %q, want /from/env", got)
	}
}

// TestLoadNativeEnvFile_MissingIsNoop verifies an absent env file (e.g. Docker)
// is silently ignored.
func TestLoadNativeEnvFile_MissingIsNoop(t *testing.T) {
	origEnvFile := nativeEnvFile
	t.Cleanup(func() { nativeEnvFile = origEnvFile })
	nativeEnvFile = filepath.Join(t.TempDir(), "missing.env")
	loadNativeEnvFile() // must not panic or error
}
