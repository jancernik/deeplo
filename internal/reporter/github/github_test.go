package github

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jancernik/deeplo/internal/reporter"
)

// ParseOwnerRepo

func TestParseOwnerRepo(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "ssh with .git",
			url:       "git@github.com:owner/myrepo.git",
			wantOwner: "owner",
			wantRepo:  "myrepo",
		},
		{
			name:      "https with .git",
			url:       "https://github.com/owner/myrepo.git",
			wantOwner: "owner",
			wantRepo:  "myrepo",
		},
		{
			name:      "https without .git",
			url:       "https://github.com/owner/myrepo",
			wantOwner: "owner",
			wantRepo:  "myrepo",
		},
		{
			name:      "ssh without .git",
			url:       "git@github.com:owner/myrepo",
			wantOwner: "owner",
			wantRepo:  "myrepo",
		},
		{
			name:      "trailing whitespace is stripped",
			url:       "  git@github.com:owner/myrepo.git  ",
			wantOwner: "owner",
			wantRepo:  "myrepo",
		},
		{
			name:      "deep path takes only first two components",
			url:       "https://github.com/owner/repo/extra.git",
			wantOwner: "owner",
			wantRepo:  "repo",
		},
		{
			name:    "non-github URL",
			url:     "git@gitlab.com:owner/repo.git",
			wantErr: true,
		},
		{
			name:    "empty string",
			url:     "",
			wantErr: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			owner, repo, err := ParseOwnerRepo(testCase.url)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("ParseOwnerRepo(%q) error = %v, wantErr %v", testCase.url, err, testCase.wantErr)
			}
			if err != nil {
				return
			}
			if owner != testCase.wantOwner {
				t.Errorf("owner: got %q, want %q", owner, testCase.wantOwner)
			}
			if repo != testCase.wantRepo {
				t.Errorf("repo: got %q, want %q", repo, testCase.wantRepo)
			}
		})
	}
}

// truncate

func TestTruncate(t *testing.T) {
	cases := []struct {
		name  string
		input string
		max   int
		want  string
	}{
		{"short string unchanged", "hello", 10, "hello"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"one over limit truncated", "hello!", 5, "hell…"},
		{"long string truncated", strings.Repeat("a", 200), 140, strings.Repeat("a", 139) + "…"},
		{"empty string unchanged", "", 10, ""},
		{"multibyte runes", "héllo", 4, "hél…"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := truncate(testCase.input, testCase.max)
			if got != testCase.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", testCase.input, testCase.max, got, testCase.want)
			}
		})
	}
}

// disabled reporter

func TestReporter_Disabled_NoOps(t *testing.T) {
	r := &Reporter{} // zero-value: token == "" → disabled

	if r.Enabled() {
		t.Error("zero-value Reporter should not be enabled")
	}

	ctx := context.Background()
	info := reporter.DeployInfo{
		RepoURL:     "https://github.com/owner/repo.git",
		CommitSHA:   "abc",
		ProjectName: "myapp",
		HostName:    "vm-1",
	}
	if token := r.DeployStarted(ctx, info, ""); token != "" {
		t.Errorf("DeployStarted on disabled reporter returned %q, want empty", token)
	}
	if err := r.DeploySucceeded(ctx, info, "99", "ok", ""); err != nil {
		t.Errorf("DeploySucceeded on disabled reporter: %v", err)
	}
	if err := r.DeployFailed(ctx, info, "99", "fail", ""); err != nil {
		t.Errorf("DeployFailed on disabled reporter: %v", err)
	}
}

// New from token file

func TestNew_EmptyTokenFile_ReturnsDisabled(t *testing.T) {
	r, err := New(Config{}, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Enabled() {
		t.Error("expected disabled reporter when TokenFile is empty")
	}
}

func TestNew_MissingFile_ReturnsError(t *testing.T) {
	_, err := New(Config{TokenFile: "/nonexistent/github_token"}, slog.Default())
	if err == nil {
		t.Error("expected error for missing token file")
	}
}

func TestNew_EmptyFileContent_ReturnsError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(f, []byte("  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{TokenFile: f}, slog.Default())
	if err == nil {
		t.Error("expected error for empty token file")
	}
}

func TestNew_ValidTokenFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(f, []byte("ghp_test_token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{TokenFile: f}, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Enabled() {
		t.Error("expected enabled reporter")
	}
}

// deploy lifecycle with test server

type capturedRequest struct {
	method string
	path   string
	body   map[string]any
}

func readBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return m
}

// newTestReporter creates a Reporter wired to a test HTTP server URL.
func newTestReporter(t *testing.T, serverURL string) *Reporter {
	t.Helper()
	return newReporter("test-token", serverURL, "", false, nil, slog.Default())
}

// testDeployInfo returns a DeployInfo pointing at a GitHub repo for testing.
func testDeployInfo(repoURL, sha, project, host string) reporter.DeployInfo {
	return reporter.DeployInfo{
		RepoURL:     repoURL,
		CommitSHA:   sha,
		ProjectName: project,
		HostName:    host,
	}
}

func TestReporter_DeployLifecycle_Success(t *testing.T) {
	var received []capturedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		received = append(received, capturedRequest{
			method: r.Method,
			path:   r.URL.Path,
			body:   body,
		})
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected Authorization: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("unexpected Accept: %q", r.Header.Get("Accept"))
		}

		if strings.HasSuffix(r.URL.Path, "/deployments") && !strings.Contains(r.URL.Path, "/statuses") {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": float64(99)})
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	r := newTestReporter(t, srv.URL)
	ctx := context.Background()
	info := testDeployInfo("https://github.com/owner/repo.git", "abc1234", "production", "vm-1")

	token := r.DeployStarted(ctx, info, "")
	if token != "99" {
		t.Errorf("DeployStarted: got token %q, want \"99\"", token)
	}

	if err := r.DeploySucceeded(ctx, info, token, "Deployed successfully", ""); err != nil {
		t.Errorf("DeploySucceeded: %v", err)
	}

	// 5 requests: create, deployment in_progress, commit pending, deployment success, commit success.
	if len(received) != 5 {
		t.Fatalf("expected 5 requests, got %d: %v", len(received), func() []string {
			var paths []string
			for _, r := range received {
				paths = append(paths, r.path)
			}
			return paths
		}())
	}

	// Request 0: create deployment
	req0 := received[0]
	if req0.path != "/repos/owner/repo/deployments" {
		t.Errorf("req[0] path: got %q", req0.path)
	}
	if req0.body["ref"] != "abc1234" {
		t.Errorf("req[0] ref: got %v", req0.body["ref"])
	}
	// environment = ProjectName (no environment base, no host prefix)
	if req0.body["environment"] != "production" {
		t.Errorf("req[0] environment: got %v", req0.body["environment"])
	}
	if req0.body["auto_merge"] != false {
		t.Errorf("req[0] auto_merge: got %v", req0.body["auto_merge"])
	}

	// Request 1: in_progress deployment status
	if received[1].path != "/repos/owner/repo/deployments/99/statuses" {
		t.Errorf("req[1] path: got %q", received[1].path)
	}
	if received[1].body["state"] != "in_progress" {
		t.Errorf("req[1] state: got %v", received[1].body["state"])
	}

	// Request 2: pending commit status
	if received[2].path != "/repos/owner/repo/statuses/abc1234" {
		t.Errorf("req[2] path: got %q", received[2].path)
	}
	if received[2].body["state"] != "pending" {
		t.Errorf("req[2] state: got %v", received[2].body["state"])
	}
	if received[2].body["context"] != "deeplo/production" {
		t.Errorf("req[2] context: got %v", received[2].body["context"])
	}

	// Request 3: success deployment status
	if received[3].path != "/repos/owner/repo/deployments/99/statuses" {
		t.Errorf("req[3] path: got %q", received[3].path)
	}
	if received[3].body["state"] != "success" {
		t.Errorf("req[3] state: got %v", received[3].body["state"])
	}

	// Request 4: success commit status
	if received[4].path != "/repos/owner/repo/statuses/abc1234" {
		t.Errorf("req[4] path: got %q", received[4].path)
	}
	if received[4].body["state"] != "success" {
		t.Errorf("req[4] state: got %v", received[4].body["state"])
	}
}

func TestReporter_DeployLifecycle_Failure(t *testing.T) {
	var states []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if s, ok := body["state"].(string); ok {
			states = append(states, s)
		}
		if strings.HasSuffix(r.URL.Path, "/deployments") && !strings.Contains(r.URL.Path, "/statuses") {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": float64(55)})
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	r := newTestReporter(t, srv.URL)
	ctx := context.Background()
	info := testDeployInfo("https://github.com/owner/repo.git", "deadbeef", "staging", "vm-1")

	token := r.DeployStarted(ctx, info, "")
	if token != "55" {
		t.Fatalf("DeployStarted: got %q, want \"55\"", token)
	}
	if err := r.DeployFailed(ctx, info, token, "compose up failed", ""); err != nil {
		t.Errorf("DeployFailed: %v", err)
	}

	// States seen: in_progress (deployment), pending (commit), failure (deployment), failure (commit)
	want := []string{"in_progress", "pending", "failure", "failure"}
	if len(states) != len(want) {
		t.Fatalf("states: got %v, want %v", states, want)
	}
	for i, s := range want {
		if states[i] != s {
			t.Errorf("states[%d]: got %q, want %q", i, states[i], s)
		}
	}
}

func TestReporter_APIError_DoesNotPanic(t *testing.T) {
	// Server returns 422 for all requests.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	r := newTestReporter(t, srv.URL)
	ctx := context.Background()
	info := testDeployInfo("https://github.com/owner/repo.git", "abc", "production", "vm-1")

	// DeployStarted returns "" on API error; does not panic or return error.
	token := r.DeployStarted(ctx, info, "")
	if token != "" {
		t.Errorf("expected empty token on API error, got %q", token)
	}

	// DeploySucceeded and DeployFailed return errors on API failure.
	err := r.DeploySucceeded(ctx, info, "", "ok", "")
	if err == nil {
		t.Error("expected error from DeploySucceeded when API returns 422")
	}
}

func TestReporter_DeployStarted_EmptyTokenWhenDeploymentCreateFails(t *testing.T) {
	// Server returns 403 for deployment creation, 201 for everything else.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/deployments") && !strings.Contains(r.URL.Path, "statuses") {
			http.Error(w, `{"message":"Resource not accessible"}`, http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	r := newTestReporter(t, srv.URL)
	info := testDeployInfo("https://github.com/owner/repo.git", "abc", "production", "vm-1")
	token := r.DeployStarted(context.Background(), info, "")
	if token != "" {
		t.Errorf("expected empty token when deployment creation fails, got %q", token)
	}
}

func TestReporter_LogURL_IncludedWhenSet(t *testing.T) {
	var logURLs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["log_url"].(string); ok && v != "" {
			logURLs = append(logURLs, v)
		}
		if v, ok := body["target_url"].(string); ok && v != "" {
			logURLs = append(logURLs, v)
		}
		if strings.HasSuffix(r.URL.Path, "/deployments") && !strings.Contains(r.URL.Path, "statuses") {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	r := newTestReporter(t, srv.URL)
	ctx := context.Background()
	info := testDeployInfo("https://github.com/owner/repo.git", "abc", "production", "vm-1")

	token := r.DeployStarted(ctx, info, "https://logs.example.com/run/123")
	_ = r.DeploySucceeded(ctx, info, token, "ok", "https://logs.example.com/run/123")

	if len(logURLs) == 0 {
		t.Error("expected log_url/target_url to be included in requests, but none found")
	}
	for _, u := range logURLs {
		if u != "https://logs.example.com/run/123" {
			t.Errorf("unexpected log URL: %q", u)
		}
	}
}

func TestReporter_AutoInactive_NeverSent(t *testing.T) {
	// auto_inactive is not sent in any request - GitHub's default (true) is used.
	var autoInactiveValues []any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["auto_inactive"]; ok {
			autoInactiveValues = append(autoInactiveValues, v)
		}
		if strings.HasSuffix(r.URL.Path, "/deployments") && !strings.Contains(r.URL.Path, "statuses") {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	r := newTestReporter(t, srv.URL)
	ctx := context.Background()
	info := testDeployInfo("https://github.com/owner/repo.git", "abc", "production", "vm-1")
	token := r.DeployStarted(ctx, info, "")
	_ = r.DeploySucceeded(ctx, info, token, "ok", "")

	if len(autoInactiveValues) != 0 {
		t.Errorf("auto_inactive should never be sent (GitHub default is true), got: %v", autoInactiveValues)
	}
}

func TestReporter_ReportingFailure_DoesNotAffectDeploy(t *testing.T) {
	// Simulate a reporter that always fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := newTestReporter(t, srv.URL)
	ctx := context.Background()
	info := testDeployInfo("https://github.com/owner/repo.git", "abc", "production", "vm-1")

	// Simulate caller: reporting failures are captured but the deploy proceeds.
	token := r.DeployStarted(ctx, info, "") // returns "", logs warning
	if token != "" {
		t.Errorf("expected empty token on failure, got %q", token)
	}

	reportErr := r.DeploySucceeded(ctx, info, token, "ok", "")
	// The deploy succeeded; only reporting failed, and the caller gets reportErr.
	if reportErr == nil {
		t.Error("expected reporting error to be returned so caller can record it")
	}
}

func TestReporter_EnvironmentNaming(t *testing.T) {
	cases := []struct {
		name            string
		environmentBase string
		environmentHost bool
		project         string
		host            string
		wantEnv         string
		wantContext     string
	}{
		{
			name:    "project only",
			project: "myapp",
			host:    "vm-1",
			wantEnv: "myapp",
		},
		{
			name:            "with base",
			environmentBase: "homelab",
			project:         "myapp",
			host:            "vm-1",
			wantEnv:         "homelab/myapp",
		},
		{
			name:            "with host",
			environmentHost: true,
			project:         "myapp",
			host:            "vm-1",
			wantEnv:         "vm-1/myapp",
		},
		{
			name:            "with base and host",
			environmentBase: "homelab",
			environmentHost: true,
			project:         "myapp",
			host:            "vm-1",
			wantEnv:         "homelab/vm-1/myapp",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			r := newReporter("tok", defaultAPIBase, testCase.environmentBase, testCase.environmentHost, nil, slog.Default())
			info := testDeployInfo("https://github.com/owner/repo.git", "sha", testCase.project, testCase.host)
			dc, err := r.deployContext(info)
			if err != nil {
				t.Fatalf("deployContext: %v", err)
			}
			if dc.environment != testCase.wantEnv {
				t.Errorf("environment: got %q, want %q", dc.environment, testCase.wantEnv)
			}
			wantCtx := "deeplo/" + testCase.wantEnv
			if dc.commitContext != wantCtx {
				t.Errorf("commitContext: got %q, want %q", dc.commitContext, wantCtx)
			}
		})
	}
}
