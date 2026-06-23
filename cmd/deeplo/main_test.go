// Integration tests that build and run the real deeplo binary.
// These verify end-to-end behavior that unit tests cannot: that errors are
// printed to stderr, exit codes are correct, and the arg-validation path
// reaches the operator.
package main_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// deeploPath holds the path to the binary built in TestMain.
var deeploPath string

// TestMain builds the binary once for the whole package.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "deeplo-inttest-")
	if err != nil {
		panic("mktemp: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	deeploPath = filepath.Join(tmp, "deeplo")
	out, err := exec.Command("go", "build", "-o", deeploPath, ".").CombinedOutput()
	if err != nil {
		panic("go build failed:\n" + string(out))
	}
	os.Exit(m.Run())
}

// runDeeplo executes the built binary and returns stdout, stderr, and exit code.
func runDeeplo(args ...string) (stdout, stderr string, exitCode int) {
	return runDeeplo2(os.Environ(), args...)
}

// runDeeplo2 is like runDeeplo but accepts a custom environment.
func runDeeplo2(env []string, args ...string) (stdout, stderr string, exitCode int) {
	cmd := exec.Command(deeploPath, args...)
	cmd.Env = env
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// runUnreachable runs the binary pointed at a socket that does not exist, so
// daemon-bound commands fail as if the daemon were down.
func runUnreachable(args ...string) (stdout, stderr string, exitCode int) {
	return runDeeplo2(append(os.Environ(), "DEEPLO_UNIX_SOCKET=/nonexistent/deeplo.sock"), args...)
}

// Command errors must appear on stderr, not be swallowed by SilenceErrors
// (config reload returns a plain error, unlike health's silent-exit path).
func TestErrorsAreSurfacedToStderr(t *testing.T) {
	_, stderr, code := runUnreachable("config", "reload")
	if code == 0 {
		t.Fatal("expected non-zero exit for unreachable daemon, got 0")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected error text on stderr for unreachable daemon, got empty\n" +
			"  root cause: SilenceErrors:true + main.go not printing errors")
	}
}

// TestLogArgValidationVisible verifies that "deeplo deploys logs" without a
// run ID prints an error on stderr and exits non-zero.
func TestLogArgValidationVisible(t *testing.T) {
	_, stderr, code := runDeeplo("deploys", "logs")
	if code == 0 {
		t.Fatal("expected non-zero exit for missing run ID, got 0")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected error text on stderr for missing run ID arg, got empty")
	}
}

// TestVersionPrintsCliVersion verifies that "deeplo version" prints the version
// line. It does not contact the daemon.
func TestVersionPrintsCliVersion(t *testing.T) {
	stdout, _, _ := runDeeplo("version")
	if !strings.HasPrefix(strings.TrimSpace(stdout), "deeplo ") {
		t.Errorf("version: expected a 'deeplo <version>' line, got %q", stdout)
	}
}

// TestHistoryDaemonUnreachable verifies that "deeplo deploys history"
// prints an error on stderr (not silently exits 1) when the daemon is
// unavailable.
func TestHistoryDaemonUnreachable(t *testing.T) {
	_, stderr, code := runUnreachable("deploys", "history")
	if code == 0 {
		t.Fatal("expected non-zero exit when daemon is unreachable")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected error on stderr, got empty")
	}
}

// TestDeploymentsDaemonUnreachable mirrors TestHistoryDaemonUnreachable for the
// recorded deployment state view.
func TestDeploymentsDaemonUnreachable(t *testing.T) {
	_, stderr, code := runUnreachable("deploys", "state")
	if code == 0 {
		t.Fatal("expected non-zero exit when daemon is unreachable")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected error on stderr, got empty")
	}
}

// TestDeploysContainersDaemonUnreachable mirrors the above for the live
// container view (formerly the "refresh" command).
func TestDeploysContainersDaemonUnreachable(t *testing.T) {
	_, stderr, code := runUnreachable("deploys", "containers")
	if code == 0 {
		t.Fatal("expected non-zero exit when daemon is unreachable")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected error on stderr for unreachable daemon, got empty")
	}
}

// TestReloadDaemonUnreachable verifies that "deeplo config reload" fails clearly.
func TestReloadDaemonUnreachable(t *testing.T) {
	_, stderr, code := runUnreachable("config", "reload")
	if code == 0 {
		t.Fatal("expected non-zero exit when daemon is unreachable")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected error on stderr for unreachable daemon, got empty")
	}
}
