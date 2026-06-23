package cli

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// runLogs runs `deploys logs` against the daemon at sock.
func runLogs(t *testing.T, sock string, args ...string) (string, error) {
	t.Helper()
	withAdminSocket(t, sock)
	out, _, err := runCmd(t, DeploysCmd(), append([]string{"deploys", "logs"}, args...))
	return out, err
}

// TestLogsFetchesFromDaemon verifies logs fetches the run log from the daemon.
func TestLogsFetchesFromDaemon(t *testing.T) {
	const logContent = "[10:00:00Z] deploy started\n[10:00:05Z] done\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(logContent))
	})
	sock := startFakeDaemon(t, mux)

	out, err := runLogs(t, sock, "1700000000-aabbccdd")
	if err != nil {
		t.Fatalf("logs: unexpected error: %v\noutput:\n%s", err, out)
	}
	if out != logContent {
		t.Errorf("logs: expected %q, got %q", logContent, out)
	}
}

// TestLogsRequiresDaemon verifies logs errors when the daemon is unreachable.
func TestLogsRequiresDaemon(t *testing.T) {
	if _, err := runLogs(t, filepath.Join(t.TempDir(), "no.sock"), "someid"); err == nil {
		t.Fatal("logs: expected error when daemon unreachable")
	}
}

// TestLogsRequiresRunID verifies logs with no argument fails with a clear usage
// error.
func TestLogsRequiresRunID(t *testing.T) {
	_, err := runLogs(t, filepath.Join(t.TempDir(), "unused.sock"))
	if err == nil {
		t.Fatal("logs (no args): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "run-id") || !strings.Contains(err.Error(), "history") {
		t.Errorf("logs (no args): error should name the missing arg and point at history, got %q", err.Error())
	}
}
