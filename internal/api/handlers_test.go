package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/state"
)

// newTestServer builds a Server with a real FileStore in a temp dir and returns
// it together with the runs dir so individual tests can seed log files. The
// optional mutate hook lets a test override Config fields (OnReload, etc.).
func newTestServer(t *testing.T, mutate func(*Config)) (*Server, string) {
	t.Helper()
	dataDir := t.TempDir()
	runsDir := filepath.Join(dataDir, "runs")
	if err := os.MkdirAll(runsDir, 0755); err != nil {
		t.Fatal(err)
	}
	store := state.NewFileStore(dataDir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}

	serverConfig := Config{
		StartedAt: time.Now().Add(-90 * time.Second),
		Version:   "1.2.3",
		Store:     store,
		RunsDir:   runsDir,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&serverConfig)
	}
	return New(serverConfig), runsDir
}

// do issues a request through the server's full mux so routing is exercised too.
func do(t *testing.T, server *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, nil)
	server.httpServer.Handler.ServeHTTP(recorder, request)
	return recorder
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return value
}

func seedDeployment(t *testing.T, store *state.FileStore, id, project, host string, startedAt time.Time) {
	t.Helper()
	if err := store.SaveDeployment(&state.Deployment{
		ID:        id,
		Project:   project,
		Host:      host,
		Status:    state.StatusSuccess,
		StartedAt: startedAt,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHandleHealth(t *testing.T) {
	server, _ := newTestServer(t, nil)
	recorder := do(t, server, http.MethodGet, "/api/v1/health")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if ct := recorder.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := decode[HealthResponse](t, recorder)
	if !body.OK {
		t.Error("OK = false, want true")
	}
	if body.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", body.Version)
	}
	if body.Uptime == "" {
		t.Error("Uptime is empty")
	}
}

func TestHandleDeployments(t *testing.T) {
	server, _ := newTestServer(t, nil)
	now := time.Now()
	seedDeployment(t, server.config.Store, "1-aaaaaaaa", "web", "prod", now)
	seedDeployment(t, server.config.Store, "2-bbbbbbbb", "api", "prod", now)

	recorder := do(t, server, http.MethodGet, "/api/v1/deployments")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decode[DeploymentsResponse](t, recorder)
	if len(body.Deployments) != 2 {
		t.Fatalf("got %d deployments, want 2", len(body.Deployments))
	}
}

func TestHandleRuns_FilterAndLimit(t *testing.T) {
	server, _ := newTestServer(t, nil)
	base := time.Now()
	seedDeployment(t, server.config.Store, "1-aaaaaaaa", "web", "prod", base.Add(-3*time.Minute))
	seedDeployment(t, server.config.Store, "2-bbbbbbbb", "web", "prod", base.Add(-2*time.Minute))
	seedDeployment(t, server.config.Store, "3-cccccccc", "api", "prod", base.Add(-1*time.Minute))

	// Filter by project.
	recorder := do(t, server, http.MethodGet, "/api/v1/runs?project=web&host=prod")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decode[RunsResponse](t, recorder)
	if len(body.Runs) != 2 {
		t.Fatalf("project filter: got %d runs, want 2", len(body.Runs))
	}
	for _, run := range body.Runs {
		if run.Project != "web" {
			t.Errorf("unexpected project %q in filtered runs", run.Project)
		}
	}

	// Limit caps the result count.
	recorder = do(t, server, http.MethodGet, "/api/v1/runs?project=web&host=prod&limit=1")
	body = decode[RunsResponse](t, recorder)
	if len(body.Runs) != 1 {
		t.Fatalf("limit=1: got %d runs, want 1", len(body.Runs))
	}
}

// TestHandleRuns_InvalidLimitFallsBack verifies a non-numeric or non-positive
// limit is ignored rather than rejected, falling back to the default.
func TestHandleRuns_InvalidLimitFallsBack(t *testing.T) {
	server, _ := newTestServer(t, nil)
	seedDeployment(t, server.config.Store, "1-aaaaaaaa", "web", "prod", time.Now())

	for _, target := range []string{
		"/api/v1/runs?limit=abc",
		"/api/v1/runs?limit=0",
		"/api/v1/runs?limit=-5",
	} {
		recorder := do(t, server, http.MethodGet, target)
		if recorder.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", target, recorder.Code)
		}
	}
}

func TestHandleRunLog_Serves(t *testing.T) {
	server, runsDir := newTestServer(t, nil)
	if err := os.WriteFile(filepath.Join(runsDir, "1-aaaaaaaa.log"), []byte("line one\n"), 0644); err != nil {
		t.Fatal(err)
	}

	recorder := do(t, server, http.MethodGet, "/api/v1/runs/1-aaaaaaaa/log")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if ct := recorder.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if nosniff := recorder.Header().Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", nosniff)
	}
	if recorder.Body.String() != "line one\n" {
		t.Errorf("body = %q", recorder.Body.String())
	}
}

func TestHandleRunLog_InvalidID(t *testing.T) {
	server, _ := newTestServer(t, nil)
	recorder := do(t, server, http.MethodGet, "/api/v1/runs/not-a-valid-id/log")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestHandleRunLog_NotFound(t *testing.T) {
	server, _ := newTestServer(t, nil)
	recorder := do(t, server, http.MethodGet, "/api/v1/runs/9-99999999/log")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestHandleRefresh_Unavailable(t *testing.T) {
	server, _ := newTestServer(t, nil) // OnRefresh nil
	recorder := do(t, server, http.MethodPost, "/api/v1/refresh")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestHandleRefresh_ReturnsHosts(t *testing.T) {
	server, _ := newTestServer(t, func(serverConfig *Config) {
		serverConfig.OnRefresh = func(context.Context) []RefreshHost {
			return []RefreshHost{{
				Host: "prod",
				Projects: []RefreshProject{{
					Project:  "web",
					Services: []RefreshService{{Service: "app", State: "running", Status: "Up"}},
				}},
			}}
		}
	})
	recorder := do(t, server, http.MethodPost, "/api/v1/refresh")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decode[RefreshResponse](t, recorder)
	if len(body.Hosts) != 1 || body.Hosts[0].Host != "prod" {
		t.Fatalf("unexpected hosts: %+v", body.Hosts)
	}
}

func TestHandleProbe_Unavailable(t *testing.T) {
	server, _ := newTestServer(t, nil) // OnProbe nil
	recorder := do(t, server, http.MethodPost, "/api/v1/probe")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestHandleProbe_ReturnsHosts(t *testing.T) {
	server, _ := newTestServer(t, func(serverConfig *Config) {
		serverConfig.OnProbe = func(context.Context) []ProbeHost {
			return []ProbeHost{
				{Host: "prod", Address: "10.0.0.1", OK: true},
				{Host: "stage", Address: "10.0.0.2", Error: "dial timeout"},
			}
		}
	})
	recorder := do(t, server, http.MethodPost, "/api/v1/probe")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decode[ProbeResponse](t, recorder)
	if len(body.Hosts) != 2 || !body.Hosts[0].OK || body.Hosts[1].Error != "dial timeout" {
		t.Fatalf("unexpected hosts: %+v", body.Hosts)
	}
}

// TestHandleReload_Unavailable verifies a nil OnReload still returns 200 with an
// explanatory message rather than an error (reload is simply not configured).
func TestHandleReload_Unavailable(t *testing.T) {
	server, _ := newTestServer(t, nil)
	recorder := do(t, server, http.MethodPost, "/api/v1/reload")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decode[ReloadResponse](t, recorder)
	if !body.OK {
		t.Error("OK = false, want true")
	}
}

func TestHandleReload_Success(t *testing.T) {
	called := false
	server, _ := newTestServer(t, func(serverConfig *Config) {
		serverConfig.OnReload = func(context.Context) error {
			called = true
			return nil
		}
	})
	recorder := do(t, server, http.MethodPost, "/api/v1/reload")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !called {
		t.Error("OnReload was not invoked")
	}
}

func TestHandleReload_Error(t *testing.T) {
	server, _ := newTestServer(t, func(serverConfig *Config) {
		serverConfig.OnReload = func(context.Context) error {
			return errors.New("bad config")
		}
	})
	recorder := do(t, server, http.MethodPost, "/api/v1/reload")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	body := decode[errorBody](t, recorder)
	if body.Error != "bad config" {
		t.Errorf("error = %q, want %q", body.Error, "bad config")
	}
}

func TestHandleDeploy_Unavailable(t *testing.T) {
	server, _ := newTestServer(t, nil) // OnDeploy nil
	recorder := do(t, server, http.MethodPost, "/api/v1/deploy?project=web")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestHandleDeploy_MissingProject(t *testing.T) {
	server, _ := newTestServer(t, func(serverConfig *Config) {
		serverConfig.OnDeploy = func(context.Context, string, string) ([]string, error) {
			t.Fatal("OnDeploy must not be called when project is missing")
			return nil, nil
		}
	})
	recorder := do(t, server, http.MethodPost, "/api/v1/deploy")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestHandleDeploy_Success(t *testing.T) {
	var gotProject, gotHost string
	server, _ := newTestServer(t, func(serverConfig *Config) {
		serverConfig.OnDeploy = func(_ context.Context, project, host string) ([]string, error) {
			gotProject, gotHost = project, host
			return []string{"web/prod"}, nil
		}
	})
	recorder := do(t, server, http.MethodPost, "/api/v1/deploy?project=web&host=prod")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", recorder.Code)
	}
	if gotProject != "web" || gotHost != "prod" {
		t.Errorf("OnDeploy got (%q, %q), want (web, prod)", gotProject, gotHost)
	}
	body := decode[DeployResponse](t, recorder)
	if len(body.Targets) != 1 || body.Targets[0] != "web/prod" {
		t.Errorf("targets = %v, want [web/prod]", body.Targets)
	}
}

func TestHandleDeploy_Error(t *testing.T) {
	server, _ := newTestServer(t, func(serverConfig *Config) {
		serverConfig.OnDeploy = func(context.Context, string, string) ([]string, error) {
			return nil, errors.New("unknown project")
		}
	})
	recorder := do(t, server, http.MethodPost, "/api/v1/deploy?project=nope")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	body := decode[errorBody](t, recorder)
	if body.Error != "unknown project" {
		t.Errorf("error = %q", body.Error)
	}
}

// TestMethodRouting verifies the mux rejects wrong methods (e.g. GET on a
// POST-only route) with 405, confirming the method-scoped patterns are wired.
func TestMethodRouting(t *testing.T) {
	server, _ := newTestServer(t, nil)
	recorder := do(t, server, http.MethodGet, "/api/v1/deploy?project=web")
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}
