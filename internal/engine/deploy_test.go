package engine_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/engine"
	"github.com/jancernik/deeplo/internal/state"
)

func makeTestStore(t *testing.T) *state.FileStore {
	t.Helper()
	s := state.NewFileStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s
}

// shouldSkipDeploy

func TestShouldSkipDeploy_NoRecord(t *testing.T) {
	store := makeTestStore(t)
	// No deployment recorded → never skip.
	if engine.ShouldSkipDeploy(store, "myapp", "h1", "abc123") {
		t.Error("expected false for missing record")
	}
}

func TestShouldSkipDeploy_DifferentSHA(t *testing.T) {
	store := makeTestStore(t)
	now := time.Now().UTC()
	if err := store.SaveDeployment(&state.Deployment{
		ID: "1", Project: "myapp", Host: "h1",
		CommitSha: "aaa", Status: state.StatusSuccess,
		StartedAt: now, CompletedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	// Different SHA → do not skip.
	if engine.ShouldSkipDeploy(store, "myapp", "h1", "bbb") {
		t.Error("expected false for different SHA")
	}
}

func TestShouldSkipDeploy_SameSHARunning(t *testing.T) {
	store := makeTestStore(t)
	if err := store.SaveDeployment(&state.Deployment{
		ID: "1", Project: "myapp", Host: "h1",
		CommitSha: "abc", Status: state.StatusRunning,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if !engine.ShouldSkipDeploy(store, "myapp", "h1", "abc") {
		t.Error("expected true for same SHA in running state")
	}
}

func TestShouldSkipDeploy_SameSHASuccess(t *testing.T) {
	store := makeTestStore(t)
	now := time.Now().UTC()
	if err := store.SaveDeployment(&state.Deployment{
		ID: "1", Project: "myapp", Host: "h1",
		CommitSha: "abc", Status: state.StatusSuccess,
		StartedAt: now, CompletedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	if !engine.ShouldSkipDeploy(store, "myapp", "h1", "abc") {
		t.Error("expected true for same SHA with success status")
	}
}

func TestShouldSkipDeploy_SameSHAFailed(t *testing.T) {
	store := makeTestStore(t)
	now := time.Now().UTC()
	if err := store.SaveDeployment(&state.Deployment{
		ID: "1", Project: "myapp", Host: "h1",
		CommitSha: "abc", Status: state.StatusFailed,
		StartedAt: now, CompletedAt: &now,
		Error: "something went wrong",
	}); err != nil {
		t.Fatal(err)
	}
	// Failed deploys ARE retried - should not skip.
	if engine.ShouldSkipDeploy(store, "myapp", "h1", "abc") {
		t.Error("expected false for same SHA with failed status (should be retried)")
	}
}

// conciseSummary

func TestConciseSummary(t *testing.T) {
	cases := []struct {
		name string
		err  error
		host string
		want string
	}{
		{
			name: "nil error → success",
			err:  nil, host: "vm-1",
			want: "Deployed successfully",
		},
		{
			name: "open repo failure",
			err:  errors.New("open repo: some git error"),
			host: "vm-1",
			want: "Git fetch failed on vm-1",
		},
		{
			name: "ensure commit failure",
			err:  errors.New("ensure commit: revision not found"),
			host: "vm-2",
			want: "Git fetch failed on vm-2",
		},
		{
			name: "build bundle failure",
			err:  errors.New("build bundle: subdir not found"),
			host: "vm-1",
			want: "Bundle error on vm-1",
		},
		{
			name: "mktemp failure",
			err:  errors.New("mktemp: no space left on device"),
			host: "vm-1",
			want: "Storage error on vm-1",
		},
		{
			name: "dial failure",
			err:  errors.New("dial vm-1 (192.168.50.33): connection refused"),
			host: "vm-1",
			want: "SSH connection failed for vm-1",
		},
		{
			name: "preflight failure",
			err:  errors.New("preflight: compose config failed:\nstdout: \nstderr: invalid hostPort: 40dd82"),
			host: "vm-1",
			want: "Preflight failed on vm-1",
		},
		{
			name: "compose up failure",
			err:  errors.New("docker compose up: exit status 1 (stderr: Error response from daemon)"),
			host: "vm-2",
			want: "Deploy failed on vm-2",
		},
		{
			name: "unknown error",
			err:  errors.New("something unexpected"),
			host: "vm-1",
			want: "Deploy failed on vm-1",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := engine.ConciseSummary(testCase.err, testCase.host)
			if got != testCase.want {
				t.Errorf("ConciseSummary(%v, %q) = %q, want %q", testCase.err, testCase.host, got, testCase.want)
			}
		})
	}
}
