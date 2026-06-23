package cli

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// runDeploys runs `deploys <args>` against the daemon at sock.
func runDeploys(t *testing.T, sock string, args ...string) (string, error) {
	t.Helper()
	withAdminSocket(t, sock)
	out, _, err := runCmd(t, DeploysCmd(), append([]string{"deploys"}, args...))
	return out, err
}

// TestDeploysStateRequiresDaemon verifies `deploys state` contacts the daemon
// and returns an error when it is unreachable.
func TestDeploysStateRequiresDaemon(t *testing.T) {
	if _, err := runDeploys(t, filepath.Join(t.TempDir(), "no.sock"), "state"); err == nil {
		t.Fatal("deploys state: expected error when daemon unreachable")
	}
}

// TestDeploysStateEmptyState verifies `deploys state` prints a helpful message
// when no deployments are recorded.
func TestDeploysStateEmptyState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/deployments", serveJSON(map[string]any{"deployments": []any{}}))
	sock := startFakeDaemon(t, mux)

	out, err := runDeploys(t, sock, "state")
	if err != nil {
		t.Fatalf("deploys state: unexpected error: %v", err)
	}
	if !strings.Contains(out, "No deployments recorded yet") {
		t.Errorf("deploys state: expected empty-state message, got:\n%s", out)
	}
}

// TestDeploysContainersEmptyState verifies that `deploys containers` queries the
// daemon's refresh endpoint and prints a clear message when no hosts exist.
func TestDeploysContainersEmptyState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/refresh", serveJSON(map[string]any{"hosts": []any{}}))
	sock := startFakeDaemon(t, mux)

	out, err := runDeploys(t, sock, "containers")
	if err != nil {
		t.Fatalf("deploys containers (empty): unexpected error: %v", err)
	}
	if !strings.Contains(out, "No hosts configured") {
		t.Errorf("deploys containers (empty): expected empty-state message, got:\n%s", out)
	}
}
