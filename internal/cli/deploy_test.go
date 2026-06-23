package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jancernik/deeplo/internal/api"
)

func TestDeployQueued(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/deploy", serveJSON(api.DeployResponse{
		Targets: []string{"web-servers/vm-1", "web-servers/vm-2"},
	}))
	sock := startFakeDaemon(t, mux)

	withAdminSocket(t, sock)
	out, _, err := runCmd(t, DeployCmd(), []string{"deploy", "web-servers"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"web-servers/vm-1", "web-servers/vm-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestDeployWithHostFlag(t *testing.T) {
	var gotQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/deploy", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(api.DeployResponse{Targets: []string{"web-servers/vm-1"}}) //nolint:errcheck
	})
	sock := startFakeDaemon(t, mux)

	withAdminSocket(t, sock)
	out, _, err := runCmd(t, DeployCmd(), []string{"deploy", "web-servers", "--host", "vm-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotQuery, "host=vm-1") {
		t.Errorf("expected host=vm-1 in query params, got: %s", gotQuery)
	}
	if !strings.Contains(out, "web-servers/vm-1") {
		t.Errorf("output missing target, got: %s", out)
	}
}

func TestDeployProjectNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/deploy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(struct{ Error string }{Error: `project "nope" not found`}) //nolint:errcheck
	})
	sock := startFakeDaemon(t, mux)

	withAdminSocket(t, sock)
	_, _, err := runCmd(t, DeployCmd(), []string{"deploy", "nope"})
	if err == nil {
		t.Fatal("expected error for unknown project, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestDeployRequiresProject(t *testing.T) {
	withAdminSocket(t, "/nonexistent.sock")
	_, _, err := runCmd(t, DeployCmd(), []string{"deploy"})
	if err == nil {
		t.Fatal("expected error when project arg is missing")
	}
}

func TestDeployDaemonUnreachable(t *testing.T) {
	withAdminSocket(t, "/nonexistent.sock")
	_, _, err := runCmd(t, DeployCmd(), []string{"deploy", "web-servers"})
	if err == nil {
		t.Fatal("expected error when daemon not reachable, got nil")
	}
}
