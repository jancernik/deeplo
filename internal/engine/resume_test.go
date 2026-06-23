package engine_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/engine"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/state"
)

func resumeTestConfig() *config.Config {
	return &config.Config{
		Hosts: []config.Host{
			{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"},
			{Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"},
		},
		Repos:    []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1", "h2"}, DeploySubdir: "app"}},
	}
}

func seedSuccess(t *testing.T, store *state.FileStore, project, host, sha string) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.SaveDeployment(&state.Deployment{
		ID: project + "-" + host + "-" + sha, Project: project, Host: host,
		CommitSha: sha, Status: state.StatusSuccess, StartedAt: now, CompletedAt: &now,
	}); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
}

func collectForced(events []planner.RepoEvent) map[string]bool {
	hosts := map[string]bool{}
	for _, ev := range events {
		for _, target := range ev.ForcedTargets {
			hosts[target.Host.Name] = true
		}
	}
	return hosts
}

func mirrorAt(sha string) func(string, string) (string, bool) {
	return func(string, string) (string, bool) { return sha, true }
}

// Only targets not already deployed at the desired commit are resumed: here h1
// succeeded at the mirror head and must be skipped, while h2 (no record) is
// dispatched. This is the dropped-on-shutdown recovery case.
func TestResume_DeploysOnlyIncompleteTargets(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)
	seedSuccess(t, store, "app", "h1", "headsha")

	var events []planner.RepoEvent
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, mirrorAt("headsha"),
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) }, slog.Default())

	hosts := collectForced(events)
	if hosts["h1"] {
		t.Error("h1 already deployed at head; should not be resumed")
	}
	if !hosts["h2"] {
		t.Error("h2 has no deployment at head; should be resumed")
	}
	for _, ev := range events {
		if ev.Source != planner.TriggerResume {
			t.Errorf("source = %q, want resume", ev.Source)
		}
		if ev.Redeploy {
			t.Error("resume events must not set Redeploy (stay idempotent)")
		}
		if ev.CommitSha != "headsha" {
			t.Errorf("sha = %q, want headsha", ev.CommitSha)
		}
	}
}

// A target deployed at an older commit than the mirror head is resumed.
func TestResume_RedeploysTargetBehindHead(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)
	seedSuccess(t, store, "app", "h1", "oldsha")
	seedSuccess(t, store, "app", "h2", "oldsha")

	var events []planner.RepoEvent
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, mirrorAt("newsha"),
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) }, slog.Default())

	hosts := collectForced(events)
	if !hosts["h1"] || !hosts["h2"] {
		t.Errorf("both targets are behind head and should be resumed, got %v", hosts)
	}
}

// When every target is already at the desired commit, nothing is dispatched.
func TestResume_NothingWhenAllUpToDate(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)
	seedSuccess(t, store, "app", "h1", "headsha")
	seedSuccess(t, store, "app", "h2", "headsha")

	dispatched := 0
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, mirrorAt("headsha"),
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ }, slog.Default())

	if dispatched != 0 {
		t.Errorf("expected no dispatch when all targets up to date, got %d", dispatched)
	}
}

// With no mirror available, the desired commit falls back to the repo's last
// deployed commit recorded in the store.
func TestResume_FallsBackToRepoState(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "statesha")
	seedSuccess(t, store, "app", "h1", "statesha") // h1 up to date at the fallback sha

	var events []planner.RepoEvent
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, noMirror,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) }, slog.Default())

	hosts := collectForced(events)
	if hosts["h1"] {
		t.Error("h1 already at fallback sha; should not be resumed")
	}
	if !hosts["h2"] {
		t.Error("h2 should be resumed at the fallback sha")
	}
	for _, ev := range events {
		if ev.CommitSha != "statesha" {
			t.Errorf("sha = %q, want statesha (repo-state fallback)", ev.CommitSha)
		}
	}
}

// A repo with neither a mirror head nor recorded state is skipped - its first
// deploy comes from a push or poll, not a resume.
func TestResume_SkipsRepoWithNoKnownCommit(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)

	dispatched := 0
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, noMirror,
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ }, slog.Default())

	if dispatched != 0 {
		t.Errorf("expected no dispatch for a repo with no known commit, got %d", dispatched)
	}
}

func TestResume_NilStore_Noop(t *testing.T) {
	dispatched := 0
	engine.ResumeIncompleteDeploys(context.Background(), resumeTestConfig(), nil, mirrorAt("x"),
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ }, slog.Default())
	if dispatched != 0 {
		t.Errorf("nil store should be a no-op, got %d dispatches", dispatched)
	}
}
