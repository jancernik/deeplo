package cli

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// runHistory runs `deploys history` against the daemon at sock.
func runHistory(t *testing.T, sock string, args ...string) (string, error) {
	t.Helper()
	withAdminSocket(t, sock)
	out, _, err := runCmd(t, DeploysCmd(), append([]string{"deploys", "history"}, args...))
	return out, err
}

// TestHistoryRequiresDaemon verifies history contacts the daemon and errors
// when it is unreachable.
func TestHistoryRequiresDaemon(t *testing.T) {
	if _, err := runHistory(t, filepath.Join(t.TempDir(), "no.sock")); err == nil {
		t.Fatal("history: expected error when daemon unreachable")
	}
}

// TestHistoryEmptyState verifies history prints a clear message when no runs
// are recorded.
func TestHistoryEmptyState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs", serveJSON(map[string]any{"runs": []any{}}))
	sock := startFakeDaemon(t, mux)

	out, err := runHistory(t, sock)
	if err != nil {
		t.Fatalf("history (empty): unexpected error: %v", err)
	}
	if !strings.Contains(out, "No runs recorded yet") {
		t.Errorf("history (empty): expected empty-state message, got:\n%s", out)
	}
}

// TestHistoryWithData verifies history prints a table with run rows.
func TestHistoryWithData(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs", serveJSON(map[string]any{
		"runs": []map[string]any{
			{
				"id":             "1744660003-a3f2bc91",
				"project":        "api",
				"host":           "web-1",
				"status":         "success",
				"commit_sha":     "a3f2bc91deadbeef",
				"trigger_source": "webhook",
				"started_at":     "2026-04-14T15:30:00Z",
			},
		},
	}))
	sock := startFakeDaemon(t, mux)

	out, err := runHistory(t, sock)
	if err != nil {
		t.Fatalf("history (with data): unexpected error: %v", err)
	}
	for _, want := range []string{"ID", "PROJECT", "api", "web-1", "success", "a3f2bc9"} {
		if !strings.Contains(out, want) {
			t.Errorf("history (with data): output missing %q, got:\n%s", want, out)
		}
	}
}

// TestHistoryForwardsProjectAndHostFilters verifies --project and --host are
// forwarded to the daemon as query parameters.
func TestHistoryForwardsProjectAndHostFilters(t *testing.T) {
	var gotProject, gotHost string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		gotProject = r.URL.Query().Get("project")
		gotHost = r.URL.Query().Get("host")
		serveJSON(map[string]any{"runs": []any{}})(w, r)
	})
	sock := startFakeDaemon(t, mux)

	if _, err := runHistory(t, sock, "--project", "api", "--host", "web-1"); err != nil {
		t.Fatalf("history (filters): unexpected error: %v", err)
	}
	if gotProject != "api" {
		t.Errorf("history: project filter = %q, want %q", gotProject, "api")
	}
	if gotHost != "web-1" {
		t.Errorf("history: host filter = %q, want %q", gotHost, "web-1")
	}
}
