package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jancernik/deeplo/internal/api"
)

// executeHealthCmd runs "health" against the daemon at sock and returns stdout, stderr, and any error.
func executeHealthCmd(t *testing.T, sock string) (stdout, stderr string, err error) {
	t.Helper()
	withAdminSocket(t, sock)
	var outBuf, errBuf bytes.Buffer
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(HealthCmd())
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"health"})
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// overrideHealthPingDaemon sets healthPingDaemon for the duration of the test.
func overrideHealthPingDaemon(t *testing.T, version, uptime string, ok bool) {
	t.Helper()
	orig := healthPingDaemon
	t.Cleanup(func() { healthPingDaemon = orig })
	healthPingDaemon = func(_ context.Context) (string, string, bool) {
		return version, uptime, ok
	}
}

// touchFile creates an empty file at path, used to simulate a present socket.
func touchFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("touch %s: %v", path, err)
	}
	return path
}

// TestHealthNativeRunning verifies the happy path: service running, daemon reachable.
func TestHealthNativeRunning(t *testing.T) {
	overrideNative(t, nil)
	overrideSystemctlExitZero(t, true)
	overrideHealthPingDaemon(t, "v0.2.0", "5m30s", true)

	sockPath := touchFile(t, filepath.Join(t.TempDir(), "test.sock"))
	stdout, _, err := executeHealthCmd(t, sockPath)
	if err != nil {
		t.Fatalf("health: unexpected error when service is running: %v", err)
	}
	for _, want := range []string{"running", "yes", "present", "reachable", "v0.2.0", "5m30s"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("health: output missing %q:\n%s", want, stdout)
		}
	}
}

// TestHealthNativeServiceStopped verifies that a stopped service exits non-zero
// and prints an action hint.
func TestHealthNativeServiceStopped(t *testing.T) {
	overrideNative(t, nil)
	overrideSystemctlExitZero(t, false)

	stdout, _, err := executeHealthCmd(t, filepath.Join(t.TempDir(), "no.sock"))
	if err == nil {
		t.Fatal("health: expected non-zero exit when service is stopped")
	}
	if !IsSilentExit(err) {
		t.Errorf("health: stopped service should return the silent-exit sentinel, got: %v", err)
	}
	if !strings.Contains(stdout, "not running") {
		t.Errorf("health: expected 'not running' in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "deeplo service start") {
		t.Errorf("health: expected 'deeplo service start' hint, got:\n%s", stdout)
	}
}

// TestHealthNativeSocketMissing verifies that a running service without a socket
// reports "not checked" and still exits 0.
func TestHealthNativeSocketMissing(t *testing.T) {
	overrideNative(t, nil)
	overrideSystemctlExitZero(t, true)

	stdout, _, err := executeHealthCmd(t, filepath.Join(t.TempDir(), "no.sock"))
	if err != nil {
		t.Fatalf("health: unexpected error when service is running but socket missing: %v", err)
	}
	if !strings.Contains(stdout, "not checked") {
		t.Errorf("health: expected 'not checked' when socket missing, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "running") {
		t.Errorf("health: expected 'running' in output, got:\n%s", stdout)
	}
}

// TestHealthNativeDaemonUnreachable verifies that a running service with a
// present socket but unresponsive daemon reports "unreachable" but exits 0.
func TestHealthNativeDaemonUnreachable(t *testing.T) {
	overrideNative(t, nil)
	overrideSystemctlExitZero(t, true)
	overrideHealthPingDaemon(t, "", "", false)

	sockPath := touchFile(t, filepath.Join(t.TempDir(), "test.sock"))
	stdout, _, err := executeHealthCmd(t, sockPath)
	if err != nil {
		t.Fatalf("health: unexpected error when service is running (even with unreachable daemon): %v", err)
	}
	if !strings.Contains(stdout, "unreachable") {
		t.Errorf("health: expected 'unreachable' when daemon is unresponsive, got:\n%s", stdout)
	}
}

// TestHealthDockerDaemonReachable verifies the Docker (non-native) happy path.
func TestHealthDockerDaemonReachable(t *testing.T) {
	overrideNative(t, errors.New("not native"))
	overrideHealthPingDaemon(t, "v0.2.0", "1h", true)

	stdout, _, err := executeHealthCmd(t, "/tmp/test.sock")
	if err != nil {
		t.Fatalf("health: unexpected error in docker happy path: %v", err)
	}
	for _, want := range []string{"reachable", "v0.2.0", "1h"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("health: missing %q in docker output:\n%s", want, stdout)
		}
	}
}

// TestHealthDockerDaemonUnreachable verifies that the Docker path exits non-zero
// and writes a helpful message to stderr.
func TestHealthDockerDaemonUnreachable(t *testing.T) {
	overrideNative(t, errors.New("not native"))
	overrideHealthPingDaemon(t, "", "", false)

	_, stderr, err := executeHealthCmd(t, filepath.Join(t.TempDir(), "no.sock"))
	if err == nil {
		t.Fatal("health: expected non-zero exit when daemon is unreachable")
	}
	if !strings.Contains(stderr, "not reachable") {
		t.Errorf("health: expected 'not reachable' on stderr, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "docker ps") {
		t.Errorf("health: expected 'docker ps' hint on stderr, got:\n%s", stderr)
	}
}

// TestHealthDaemonOutputSuccess verifies health output when the daemon responds
// normally via the socket (end-to-end with a fake HTTP server).
func TestHealthDaemonOutputSuccess(t *testing.T) {
	overrideNative(t, errNotNative)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", serveJSON(api.HealthResponse{
		OK:      true,
		Version: "v1.2.3",
		Uptime:  "4h32m15s",
	}))
	sock := startFakeDaemon(t, mux)

	withAdminSocket(t, sock)
	out, _, err := runCmd(t, HealthCmd(), []string{"health"})
	if err != nil {
		t.Fatalf("health: unexpected error: %v\noutput:\n%s", err, out)
	}
	for _, want := range []string{"reachable", "v1.2.3", "4h32m15s"} {
		if !strings.Contains(out, want) {
			t.Errorf("health: output missing %q, got:\n%s", want, out)
		}
	}
}

// TestHealthDaemonOutputFailure verifies health exits non-zero when the daemon
// socket does not exist.
func TestHealthDaemonOutputFailure(t *testing.T) {
	overrideNative(t, errNotNative)
	withAdminSocket(t, filepath.Join(t.TempDir(), "missing.sock"))
	_, _, err := runCmd(t, HealthCmd(), []string{"health"})
	if err == nil {
		t.Fatal("health: expected error when daemon unreachable, got nil")
	}
}
