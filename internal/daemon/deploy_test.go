package daemon

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/state"
)

func makeRedeployConfig() *config.Config {
	return &config.Config{
		Repos: []config.RepoConfig{
			{Name: "myrepo", URL: "git@github.com:org/myrepo.git", Branch: "main"},
		},
		Hosts: []config.Host{
			{Name: "vm-1", Address: "host1", DeployDir: "/srv"},
			{Name: "vm-2", Address: "host2", DeployDir: "/srv"},
		},
		Projects: []config.Project{
			{Name: "api", Repo: "myrepo", Targets: []string{"vm-1", "vm-2"}},
			{Name: "web", Repo: "myrepo", Targets: []string{"vm-1"}},
		},
	}
}

func TestBuildRedeployFuncAllHosts(t *testing.T) {
	cfg := makeRedeployConfig()
	var gotEvent planner.RepoEvent
	redeploy := buildDeployFunc(context.Background(),
		func() *config.Config { return cfg },
		state.NewFileStore(t.TempDir()),
		func(_, _ string) (string, bool) { return "abc123def456", true },
		func(_ context.Context, event planner.RepoEvent) { gotEvent = event },
		slog.Default(),
	)

	targets, err := redeploy(context.Background(), "api", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d: %v", len(targets), targets)
	}
	if gotEvent.Source != planner.TriggerRedeploy {
		t.Errorf("expected source %q, got %q", planner.TriggerRedeploy, gotEvent.Source)
	}
	if !gotEvent.Redeploy {
		t.Error("expected Redeploy=true")
	}
	if gotEvent.CommitSha != "abc123def456" {
		t.Errorf("expected sha abc123def456, got %s", gotEvent.CommitSha)
	}
	if len(gotEvent.ForcedTargets) != 2 {
		t.Errorf("expected 2 forced targets, got %d", len(gotEvent.ForcedTargets))
	}
}

func TestBuildRedeployFuncSingleHost(t *testing.T) {
	cfg := makeRedeployConfig()
	var gotEvent planner.RepoEvent
	redeploy := buildDeployFunc(context.Background(),
		func() *config.Config { return cfg },
		state.NewFileStore(t.TempDir()),
		func(_, _ string) (string, bool) { return "abc123def456", true },
		func(_ context.Context, event planner.RepoEvent) { gotEvent = event },
		slog.Default(),
	)

	targets, err := redeploy(context.Background(), "api", "vm-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(targets) != 1 || targets[0] != "api/vm-1" {
		t.Errorf("expected [api/vm-1], got %v", targets)
	}
	if len(gotEvent.ForcedTargets) != 1 {
		t.Errorf("expected 1 forced target, got %d", len(gotEvent.ForcedTargets))
	}
}

func TestBuildRedeployFuncProjectNotFound(t *testing.T) {
	cfg := makeRedeployConfig()
	redeploy := buildDeployFunc(context.Background(),
		func() *config.Config { return cfg },
		state.NewFileStore(t.TempDir()),
		func(_, _ string) (string, bool) { return "", false },
		func(_ context.Context, _ planner.RepoEvent) {},
		slog.Default(),
	)

	_, err := redeploy(context.Background(), "nonexistent", "")
	if err == nil {
		t.Fatal("expected error for unknown project")
	}
}

func TestBuildRedeployFuncHostNotATarget(t *testing.T) {
	cfg := makeRedeployConfig()
	redeploy := buildDeployFunc(context.Background(),
		func() *config.Config { return cfg },
		state.NewFileStore(t.TempDir()),
		func(_, _ string) (string, bool) { return "abc123def456", true },
		func(_ context.Context, _ planner.RepoEvent) {},
		slog.Default(),
	)

	_, err := redeploy(context.Background(), "web", "vm-2") // web only targets vm-1
	if err == nil {
		t.Fatal("expected error when host is not a target of the project")
	}
}

func TestBuildRedeployFuncNoKnownSHA(t *testing.T) {
	cfg := makeRedeployConfig()
	redeploy := buildDeployFunc(context.Background(),
		func() *config.Config { return cfg },
		state.NewFileStore(t.TempDir()), // empty store: no repo state
		func(_, _ string) (string, bool) { return "", false },
		func(_ context.Context, _ planner.RepoEvent) {},
		slog.Default(),
	)

	_, err := redeploy(context.Background(), "api", "")
	if err == nil {
		t.Fatal("expected error when no commit SHA is known")
	}
}

func TestBuildRedeployFuncFallsBackToStoresha(t *testing.T) {
	cfg := makeRedeployConfig()
	store := state.NewFileStore(t.TempDir())
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRepoState(&state.RepoState{Repo: "myrepo", Branch: "main", LastDeployedSha: "storedsha123"}); err != nil {
		t.Fatal(err)
	}

	var gotEvent planner.RepoEvent
	redeploy := buildDeployFunc(context.Background(),
		func() *config.Config { return cfg },
		store,
		func(_, _ string) (string, bool) { return "", false }, // mirror unavailable
		func(_ context.Context, event planner.RepoEvent) { gotEvent = event },
		slog.Default(),
	)

	_, err := redeploy(context.Background(), "api", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotEvent.CommitSha != "storedsha123" {
		t.Errorf("expected sha from store, got %s", gotEvent.CommitSha)
	}
}

// TestBuildDeployFuncDispatchesOnDeployCtx is a regression test: a manual deploy
// runs asynchronously and outlives the HTTP request, so it must be dispatched on
// the daemon's long-lived context, not the request context. Otherwise the
// request returning 202 cancels the deploy before the runner starts it.
func TestBuildDeployFuncDispatchesOnDeployCtx(t *testing.T) {
	cfg := makeRedeployConfig()
	var gotCtx context.Context
	deploy := buildDeployFunc(context.Background(),
		func() *config.Config { return cfg },
		state.NewFileStore(t.TempDir()),
		func(_, _ string) (string, bool) { return "abc123def456", true },
		func(ctx context.Context, _ planner.RepoEvent) { gotCtx = ctx },
		slog.Default(),
	)

	// Simulate the HTTP request context already cancelled (request returned 202).
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := deploy(requestCtx, "api", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCtx == nil {
		t.Fatal("onDeploy was not called")
	}
	if gotCtx.Err() != nil {
		t.Fatalf("deploy dispatched on a cancelled context: %v", gotCtx.Err())
	}
}
