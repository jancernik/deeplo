package engine_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/engine"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/runner"
	"github.com/jancernik/deeplo/internal/ssh"
	"github.com/jancernik/deeplo/internal/state"
)

// startTestRunner returns a started runner (drained, stopped at cleanup) so
// teardown jobs share the same per-(project, host) lock as deploys.
func startTestRunner(t *testing.T) *runner.Runner {
	t.Helper()
	jobRunner := runner.New(runner.Config{MaxWorkers: 4, MaxHostConcurrency: 2}, slog.Default())
	jobRunner.Start()
	go engine.DrainResults(jobRunner.Results())
	t.Cleanup(jobRunner.Stop)
	return jobRunner
}

// staticConfig returns a getConfig closure that always reports config, used as
// the level-trigger source in teardown tests.
func staticConfig(deployConfig *config.Config) func() *config.Config {
	return func() *config.Config { return deployConfig }
}

// FindTeardownTargets

func TestFindTeardownTargets_NoChanges(t *testing.T) {
	deployConfig := reconcileTestConfig()
	targets := engine.FindTeardownTargets(deployConfig, deployConfig)
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets for identical configs, got %d: %+v", len(targets), targets)
	}
}

func TestFindTeardownTargets_ProjectRemoved(t *testing.T) {
	oldConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, ComposeFiles: []string{"docker-compose.yml"}}},
	}
	newConfig := &config.Config{Hosts: reconcileTestHosts()} // project gone

	targets := engine.FindTeardownTargets(oldConfig, newConfig)
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d: %+v", len(targets), targets)
	}
	if targets[0].ProjectName != "app" || targets[0].Host.Name != "h1" {
		t.Errorf("unexpected target: %+v", targets[0])
	}
}

func TestFindTeardownTargets_HostRemovedFromTargets(t *testing.T) {
	hosts := []config.Host{
		{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"},
		{Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"},
	}
	oldConfig := &config.Config{
		Hosts:    hosts,
		Projects: []config.Project{{Name: "app", Targets: []string{"h1", "h2"}, DeploySubdir: "app"}},
	}
	newConfig := &config.Config{
		Hosts:    hosts,
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "app"}}, // h2 removed
	}

	targets := engine.FindTeardownTargets(oldConfig, newConfig)
	if len(targets) != 1 {
		t.Fatalf("expected 1 target (h2), got %d: %+v", len(targets), targets)
	}
	if targets[0].Host.Name != "h2" {
		t.Errorf("expected teardown target h2, got %s", targets[0].Host.Name)
	}
}

func TestFindTeardownTargets_DeploySubdirChanged(t *testing.T) {
	hosts := reconcileTestHosts()
	oldConfig := &config.Config{
		Hosts:    hosts,
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "old-dir"}},
	}
	newConfig := &config.Config{
		Hosts:    hosts,
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "new-dir"}},
	}

	targets := engine.FindTeardownTargets(oldConfig, newConfig)
	if len(targets) != 1 {
		t.Fatalf("expected 1 target for dir rename, got %d: %+v", len(targets), targets)
	}
	if targets[0].RemoteDir != "/srv/old-dir" {
		t.Errorf("expected old remote dir /srv/old-dir, got %s", targets[0].RemoteDir)
	}
}

// A host's deploy_dir change relocates the deployment, so the old directory
// (deploy_dir/deploy_subdir) must be torn down. The record is kept, since the
// same target is redeployed at the new directory.
func TestFindTeardownTargets_HostDeployDirChanged(t *testing.T) {
	oldConfig := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/opt/apps"}},
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "app"}},
	}
	newConfig := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "app"}},
	}

	targets := engine.FindTeardownTargets(oldConfig, newConfig)
	if len(targets) != 1 {
		t.Fatalf("expected 1 teardown for deploy_dir change, got %d: %+v", len(targets), targets)
	}
	if targets[0].RemoteDir != "/opt/apps/app" {
		t.Errorf("RemoteDir = %s, want /opt/apps/app (the old location)", targets[0].RemoteDir)
	}
	if targets[0].RemoveState {
		t.Error("RemoveState should be false: the target is relocated, not removed")
	}
}

func TestFindTeardownTargets_DeploySubdirUnchanged_NoTeardown(t *testing.T) {
	hosts := reconcileTestHosts()
	oldConfig := &config.Config{
		Hosts:    hosts,
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "myapp"}},
	}
	newConfig := &config.Config{
		Hosts:    hosts,
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "myapp"}},
	}

	targets := engine.FindTeardownTargets(oldConfig, newConfig)
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets for unchanged dir, got %d: %+v", len(targets), targets)
	}
}

func TestFindTeardownTargets_MultipleHostsOneRemoved(t *testing.T) {
	hosts := []config.Host{
		{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"},
		{Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"},
		{Name: "h3", Address: "10.0.0.3", DeployDir: "/srv"},
	}
	oldConfig := &config.Config{
		Hosts:    hosts,
		Projects: []config.Project{{Name: "app", Targets: []string{"h1", "h2", "h3"}, DeploySubdir: "app"}},
	}
	newConfig := &config.Config{
		Hosts:    hosts,
		Projects: []config.Project{{Name: "app", Targets: []string{"h1", "h3"}, DeploySubdir: "app"}}, // h2 removed
	}

	targets := engine.FindTeardownTargets(oldConfig, newConfig)
	if len(targets) != 1 {
		t.Fatalf("expected 1 teardown target (h2), got %d: %+v", len(targets), targets)
	}
	if targets[0].Host.Name != "h2" {
		t.Errorf("expected h2, got %s", targets[0].Host.Name)
	}
}

func TestFindTeardownTargets_ProjectRemovedAllHostsTornDown(t *testing.T) {
	hosts := []config.Host{
		{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"},
		{Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"},
	}
	oldConfig := &config.Config{
		Hosts:    hosts,
		Projects: []config.Project{{Name: "app", Targets: []string{"h1", "h2"}}},
	}
	newConfig := &config.Config{Hosts: hosts}

	targets := engine.FindTeardownTargets(oldConfig, newConfig)
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets (all hosts), got %d: %+v", len(targets), targets)
	}
}

func TestFindTeardownTargets_OldHostNotInHostsMap_Skipped(t *testing.T) {
	// A target referencing an unknown host has no address, so no teardown target.
	oldConfig := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Projects: []config.Project{{Name: "app", Targets: []string{"h1", "missing-host"}}},
	}
	newConfig := &config.Config{} // project removed

	targets := engine.FindTeardownTargets(oldConfig, newConfig)
	for _, target := range targets {
		if target.Host.Name == "missing-host" {
			t.Errorf("expected missing-host to be skipped, but got teardown target: %+v", target)
		}
	}
}

// ReconcileRemovals

type reconcileMockDialer struct {
	dialCount atomic.Int32
	dialErr   error
	runErr    error
}

func (dialer *reconcileMockDialer) Dial(_ context.Context, _ ssh.DialConfig) (ssh.Connection, error) {
	if dialer.dialErr != nil {
		return nil, dialer.dialErr
	}
	dialer.dialCount.Add(1)
	return &reconcileMockConn{runErr: dialer.runErr}, nil
}

type reconcileMockConn struct {
	runErr error
}

func (conn *reconcileMockConn) Run(_ context.Context, _ string) (string, string, error) {
	return "", "", conn.runErr
}
func (conn *reconcileMockConn) Upload(_ context.Context, _, _ string) error { return nil }
func (conn *reconcileMockConn) Close() error                                { return nil }

func seedReconcileDeployment(t *testing.T, store *state.FileStore, project, host string) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.SaveDeployment(&state.Deployment{
		ID: project + "-" + host, Project: project, Host: host,
		CommitSha: "abc123", Status: state.StatusSuccess,
		StartedAt: now, CompletedAt: &now,
	}); err != nil {
		t.Fatalf("seed deployment %s/%s: %v", project, host, err)
	}
}

func TestReconcileRemovals_SkipsTargetWithNoDeploymentRecord(t *testing.T) {
	oldConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}}},
	}
	newConfig := &config.Config{}

	dialer := &reconcileMockDialer{}
	store := makeTestStore(t) // reuse helper from engine_test.go

	engine.ReconcileRemovals(context.Background(), oldConfig, newConfig,
		&bootstrap.Config{}, store, dialer, startTestRunner(t), staticConfig(newConfig), slog.Default())

	if dialer.dialCount.Load() != 0 {
		t.Errorf("expected 0 dials (no deployment record), got %d", dialer.dialCount.Load())
	}
}

func TestReconcileRemovals_TeardownCalledForDeployedTarget(t *testing.T) {
	oldConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}}},
	}
	newConfig := &config.Config{}

	dialer := &reconcileMockDialer{}
	store := makeTestStore(t)
	seedReconcileDeployment(t, store, "app", "h1")

	engine.ReconcileRemovals(context.Background(), oldConfig, newConfig,
		&bootstrap.Config{}, store, dialer, startTestRunner(t), staticConfig(newConfig), slog.Default())

	if dialer.dialCount.Load() != 1 {
		t.Errorf("expected 1 dial, got %d", dialer.dialCount.Load())
	}
}

func TestReconcileRemovals_TeardownFailureDoesNotBlockOthers(t *testing.T) {
	hosts := []config.Host{
		{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"},
		{Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"},
	}
	oldConfig := &config.Config{
		Hosts:    hosts,
		Projects: []config.Project{{Name: "app", Targets: []string{"h1", "h2"}}},
	}
	newConfig := &config.Config{}

	// Commands fail but the dialer succeeds - teardown should be attempted for both.
	dialer := &reconcileMockDialer{runErr: errors.New("connection reset")}
	store := makeTestStore(t)
	seedReconcileDeployment(t, store, "app", "h1")
	seedReconcileDeployment(t, store, "app", "h2")

	engine.ReconcileRemovals(context.Background(), oldConfig, newConfig,
		&bootstrap.Config{}, store, dialer, startTestRunner(t), staticConfig(newConfig), slog.Default())

	if dialer.dialCount.Load() != 2 {
		t.Errorf("expected 2 dials despite failures, got %d", dialer.dialCount.Load())
	}
}

func TestReconcileRemovals_NilStore_SkipsAll(t *testing.T) {
	oldConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}}},
	}
	newConfig := &config.Config{}

	dialer := &reconcileMockDialer{}

	engine.ReconcileRemovals(context.Background(), oldConfig, newConfig,
		&bootstrap.Config{}, nil, dialer, startTestRunner(t), staticConfig(newConfig), slog.Default())

	if dialer.dialCount.Load() != 0 {
		t.Errorf("expected 0 dials with nil store, got %d", dialer.dialCount.Load())
	}
}

func TestReconcileRemovals_NoTargets_Noop(t *testing.T) {
	deployConfig := reconcileTestConfig()
	dialer := &reconcileMockDialer{}

	// Same config old and new - no teardown targets.
	engine.ReconcileRemovals(context.Background(), deployConfig, deployConfig,
		&bootstrap.Config{}, nil, dialer, startTestRunner(t), staticConfig(deployConfig), slog.Default())

	if dialer.dialCount.Load() != 0 {
		t.Errorf("expected 0 dials for identical configs, got %d", dialer.dialCount.Load())
	}
}

// RemoveState classification

func findTeardownFor(targets []engine.TeardownTarget, project, host string) (engine.TeardownTarget, bool) {
	for _, target := range targets {
		if target.ProjectName == project && target.Host.Name == host {
			return target, true
		}
	}
	return engine.TeardownTarget{}, false
}

func TestFindTeardownTargets_RemoveState_ProjectRemoved(t *testing.T) {
	oldConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "app"}},
	}
	newConfig := &config.Config{Hosts: reconcileTestHosts()}

	target, ok := findTeardownFor(engine.FindTeardownTargets(oldConfig, newConfig), "app", "h1")
	if !ok {
		t.Fatal("expected a teardown target for removed project")
	}
	if !target.RemoveState {
		t.Error("RemoveState should be true when the project is removed entirely")
	}
}

func TestFindTeardownTargets_RemoveState_HostRemoved(t *testing.T) {
	hosts := []config.Host{
		{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"},
		{Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"},
	}
	oldConfig := &config.Config{Hosts: hosts, Projects: []config.Project{{Name: "app", Targets: []string{"h1", "h2"}, DeploySubdir: "app"}}}
	newConfig := &config.Config{Hosts: hosts, Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "app"}}}

	target, ok := findTeardownFor(engine.FindTeardownTargets(oldConfig, newConfig), "app", "h2")
	if !ok {
		t.Fatal("expected a teardown target for the removed host")
	}
	if !target.RemoveState {
		t.Error("RemoveState should be true when the host is removed from targets")
	}
}

func TestFindTeardownTargets_RemoveState_RenameKeepsState(t *testing.T) {
	oldConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "old"}},
	}
	newConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "new"}},
	}

	target, ok := findTeardownFor(engine.FindTeardownTargets(oldConfig, newConfig), "app", "h1")
	if !ok {
		t.Fatal("expected a teardown target for the old path on rename")
	}
	if target.RemoveState {
		t.Error("RemoveState should be false on a deploy_subdir rename (pair still exists)")
	}
	if target.RemoteDir != "/srv/old" {
		t.Errorf("teardown RemoteDir = %q, want /srv/old (the old path)", target.RemoteDir)
	}
}

// ReconcileRemovals: level-triggered behavior

// If the latest config re-added the target at the same path, a queued teardown
// is stale and must be skipped so the deployment survives.
func TestReconcileRemovals_SkipsTeardownWhenReAddedSamePath(t *testing.T) {
	oldConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "app"}},
	}
	removed := &config.Config{Hosts: reconcileTestHosts()}
	// Latest config re-added app/h1 at the same subdir.
	reAdded := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "app"}},
	}

	dialer := &reconcileMockDialer{}
	store := makeTestStore(t)
	seedReconcileDeployment(t, store, "app", "h1")

	engine.ReconcileRemovals(context.Background(), oldConfig, removed,
		&bootstrap.Config{}, store, dialer, startTestRunner(t), staticConfig(reAdded), slog.Default())

	if dialer.dialCount.Load() != 0 {
		t.Errorf("expected teardown to be skipped (target re-added), got %d dials", dialer.dialCount.Load())
	}
	latest, err := store.GetLatestDeployment("app", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil {
		t.Error("deployment record should survive a skipped teardown")
	}
}

// A genuine removal tears down and deletes the state record.
func TestReconcileRemovals_GenuineRemovalDeletesState(t *testing.T) {
	oldConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "app"}},
	}
	removed := &config.Config{Hosts: reconcileTestHosts()}

	dialer := &reconcileMockDialer{}
	store := makeTestStore(t)
	seedReconcileDeployment(t, store, "app", "h1")

	engine.ReconcileRemovals(context.Background(), oldConfig, removed,
		&bootstrap.Config{}, store, dialer, startTestRunner(t), staticConfig(removed), slog.Default())

	if dialer.dialCount.Load() != 1 {
		t.Errorf("expected 1 teardown dial, got %d", dialer.dialCount.Load())
	}
	latest, err := store.GetLatestDeployment("app", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if latest != nil {
		t.Errorf("state record should be deleted after genuine removal, got %v", latest.ID)
	}
}

// A deploy_subdir rename tears down the old path but keeps the state record,
// which the redeploy at the new path owns.
func TestReconcileRemovals_RenameTearsDownOldPathKeepsState(t *testing.T) {
	oldConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "old"}},
	}
	renamed := &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "new"}},
	}

	dialer := &reconcileMockDialer{}
	store := makeTestStore(t)
	seedReconcileDeployment(t, store, "app", "h1")

	engine.ReconcileRemovals(context.Background(), oldConfig, renamed,
		&bootstrap.Config{}, store, dialer, startTestRunner(t), staticConfig(renamed), slog.Default())

	if dialer.dialCount.Load() != 1 {
		t.Errorf("expected the old path to be torn down (1 dial), got %d", dialer.dialCount.Load())
	}
	latest, err := store.GetLatestDeployment("app", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil {
		t.Error("state record should be preserved across a rename teardown")
	}
}

// ReconcileAdditions

func seedRepoState(t *testing.T, store *state.FileStore, repoName, branch, sha string) {
	t.Helper()
	if err := store.SaveRepoState(&state.RepoState{
		Repo: repoName, Branch: branch,
		LastDeployedSha: sha,
	}); err != nil {
		t.Fatalf("seed repo state %s/%s: %v", repoName, branch, err)
	}
}

// noMirror is a getMirrorHead stub that reports the mirror as not available.
func noMirror(_, _ string) (string, bool) { return "", false }

func TestReconcileAdditions_NoAddedRepos_NoDeploy(t *testing.T) {
	deployConfig := reconcileTestConfig()
	var dispatched int
	engine.ReconcileAdditions(context.Background(), deployConfig, deployConfig, makeTestStore(t), noMirror,
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ },
		slog.Default())
	if dispatched != 0 {
		t.Errorf("expected 0 dispatches for identical configs, got %d", dispatched)
	}
}

func TestReconcileAdditions_NewTarget_DispatchesWithForcedTargets(t *testing.T) {
	h1 := config.Host{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}
	h2 := config.Host{Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"}
	oldConfig := &config.Config{
		Hosts:    []config.Host{h1},
		Repos:    []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}
	newConfig := &config.Config{
		Hosts: []config.Host{h1, h2},
		Repos: oldConfig.Repos,
		Projects: []config.Project{
			{Name: "app", Repo: "myrepo", Targets: []string{"h1", "h2"}},
		},
	}

	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "abc123")

	mirrorAtSameSHA := func(_, _ string) (string, bool) { return "abc123", true }

	var events []planner.RepoEvent
	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, store, mirrorAtSameSHA,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(events))
	}
	if events[0].RepoName != "myrepo" {
		t.Errorf("repo = %q, want myrepo", events[0].RepoName)
	}
	if events[0].CommitSha != "abc123" {
		t.Errorf("sha = %q, want abc123", events[0].CommitSha)
	}
	if len(events[0].ForcedTargets) != 1 {
		t.Fatalf("ForcedTargets len = %d, want 1 (only h2 is new)", len(events[0].ForcedTargets))
	}
	if events[0].ForcedTargets[0].Host.Name != "h2" {
		t.Errorf("ForcedTargets[0].Host.Name = %q, want h2", events[0].ForcedTargets[0].Host.Name)
	}
	if events[0].ForcedTargets[0].Project.Name != "app" {
		t.Errorf("ForcedTargets[0].Project.Name = %q, want app", events[0].ForcedTargets[0].Project.Name)
	}
	if events[0].ChangedFiles != nil {
		t.Errorf("ChangedFiles should be nil, got %v", events[0].ChangedFiles)
	}
	if events[0].Source != planner.TriggerReconcileAddition {
		t.Errorf("Source = %q, want %q", events[0].Source, planner.TriggerReconcileAddition)
	}
}

func TestReconcileAdditions_NoRepoState_Skips(t *testing.T) {
	oldConfig := &config.Config{Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}}}
	newConfig := &config.Config{
		Repos:    oldConfig.Repos,
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}

	var dispatched int
	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, makeTestStore(t), noMirror,
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ },
		slog.Default())

	if dispatched != 0 {
		t.Errorf("expected 0 dispatches when no repo state, got %d", dispatched)
	}
}

func TestReconcileAdditions_NilStore_Noop(t *testing.T) {
	oldConfig := &config.Config{}
	newConfig := &config.Config{
		Repos:    []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}

	var dispatched int
	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, nil, noMirror,
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ },
		slog.Default())

	if dispatched != 0 {
		t.Errorf("expected 0 dispatches with nil store, got %d", dispatched)
	}
}

func TestReconcileAdditions_MirrorAhead_DeploysAtMirrorHead(t *testing.T) {
	// Config and compose added in one commit, mirror ahead of LastDeployedSha:
	// deploy at mirror HEAD so the new files are present, not the stale SHA.
	oldConfig := &config.Config{Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}}}
	newConfig := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Repos:    oldConfig.Repos,
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}

	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "oldsha")

	mirrorAhead := func(_, _ string) (string, bool) { return "newsha", true }

	var events []planner.RepoEvent
	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, store, mirrorAhead,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch when mirror is ahead, got %d", len(events))
	}
	if events[0].CommitSha != "newsha" {
		t.Errorf("sha = %q, want newsha (mirror head)", events[0].CommitSha)
	}
}

func TestReconcileAdditions_MirrorAvailable_UsesMirrorNotStore(t *testing.T) {
	// Mirror HEAD takes priority over LastDeployedSha even when they differ.
	oldConfig := &config.Config{Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}}}
	newConfig := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Repos:    oldConfig.Repos,
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}

	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "storeSHA")

	mirrorHead := func(_, _ string) (string, bool) { return "mirrorSHA", true }

	var events []planner.RepoEvent
	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, store, mirrorHead,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(events))
	}
	if events[0].CommitSha != "mirrorSHA" {
		t.Errorf("sha = %q, want mirrorSHA", events[0].CommitSha)
	}
}

func TestReconcileAdditions_MirrorUnknown_FallsBackToLastDeployedSha(t *testing.T) {
	// Mirror not cloned yet: fall back to LastDeployedSha to bootstrap the target.
	oldConfig := &config.Config{Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}}}
	newConfig := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Repos:    oldConfig.Repos,
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}

	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "abc123")

	var events []planner.RepoEvent
	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, store, noMirror,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch when mirror is unknown, got %d", len(events))
	}
	if events[0].CommitSha != "abc123" {
		t.Errorf("sha = %q, want abc123 (last deployed sha fallback)", events[0].CommitSha)
	}
}

func TestReconcileAdditions_MirrorEmptySHA_FallsBackToLastDeployedSha(t *testing.T) {
	// getMirrorHead ok but empty SHA: fall back to LastDeployedSha, not an empty dispatch.
	oldConfig := &config.Config{Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}}}
	newConfig := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Repos:    oldConfig.Repos,
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}

	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "abc123")

	emptySHA := func(_, _ string) (string, bool) { return "", true }

	var events []planner.RepoEvent
	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, store, emptySHA,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(events))
	}
	if events[0].CommitSha != "abc123" {
		t.Errorf("sha = %q, want abc123 (fallback when mirror returns empty SHA)", events[0].CommitSha)
	}
}

func TestReconcileAdditions_MirrorAvailableNoStoreState_DeploysAtMirrorHead(t *testing.T) {
	// Brand-new repo: mirror cloned, no store state yet → dispatch at mirror HEAD.
	oldConfig := &config.Config{}
	newConfig := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Repos:    []config.RepoConfig{{Name: "myrepo", URL: "git@github.com:org/repo.git", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}

	mirrorHead := func(_, _ string) (string, bool) { return "freshsha", true }

	var events []planner.RepoEvent
	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, makeTestStore(t), mirrorHead,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch for new repo with mirror available, got %d", len(events))
	}
	if events[0].CommitSha != "freshsha" {
		t.Errorf("sha = %q, want freshsha", events[0].CommitSha)
	}
}

func TestReconcileAdditions_getMirrorHead_CalledWithRepoURL(t *testing.T) {
	// getMirrorHead must receive the repo URL, not the repo name.
	oldConfig := &config.Config{}
	newConfig := &config.Config{
		Hosts: []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Repos: []config.RepoConfig{{
			Name: "myrepo", URL: "git@github.com:org/repo.git", Branch: "main",
		}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}

	var gotURL, gotBranch string
	spy := func(url, branch string) (string, bool) {
		gotURL = url
		gotBranch = branch
		return "sha1", true
	}

	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, makeTestStore(t), spy,
		func(_ context.Context, _ planner.RepoEvent) {},
		slog.Default())

	if gotURL != "git@github.com:org/repo.git" {
		t.Errorf("getMirrorHead called with URL %q, want git@github.com:org/repo.git", gotURL)
	}
	if gotBranch != "main" {
		t.Errorf("getMirrorHead called with branch %q, want main", gotBranch)
	}
}

func TestReconcileAdditions_TwoDistinctRepos_TwoEvents(t *testing.T) {
	// Each repo with new targets dispatches its own event, keyed by repo URL so the right SHA is used.
	oldConfig := &config.Config{}
	newConfig := &config.Config{
		Hosts: []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Repos: []config.RepoConfig{
			{Name: "repo-a", URL: "git@github.com:org/a.git", Branch: "main"},
			{Name: "repo-b", URL: "git@github.com:org/b.git", Branch: "main"},
		},
		Projects: []config.Project{
			{Name: "app-a", Repo: "repo-a", Targets: []string{"h1"}},
			{Name: "app-b", Repo: "repo-b", Targets: []string{"h1"}},
		},
	}

	mirrorHead := func(url, _ string) (string, bool) {
		switch url {
		case "git@github.com:org/a.git":
			return "sha-a", true
		case "git@github.com:org/b.git":
			return "sha-b", true
		}
		return "", false
	}

	var events []planner.RepoEvent
	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, makeTestStore(t), mirrorHead,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 2 {
		t.Fatalf("expected 2 dispatch events for 2 repos, got %d", len(events))
	}
	byRepo := make(map[string]planner.RepoEvent, 2)
	for _, ev := range events {
		byRepo[ev.RepoName] = ev
	}
	if byRepo["repo-a"].CommitSha != "sha-a" {
		t.Errorf("repo-a sha = %q, want sha-a", byRepo["repo-a"].CommitSha)
	}
	if byRepo["repo-b"].CommitSha != "sha-b" {
		t.Errorf("repo-b sha = %q, want sha-b", byRepo["repo-b"].CommitSha)
	}
}

func TestReconcileAdditions_MultipleNewTargetsSameRepo_SingleDispatch(t *testing.T) {
	// Two new pairs under one repo produce a single event with both in ForcedTargets.
	h1 := config.Host{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}
	h2 := config.Host{Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"}
	oldConfig := &config.Config{}
	newConfig := &config.Config{
		Hosts: []config.Host{h1, h2},
		Repos: []config.RepoConfig{{Name: "myrepo", URL: "git@github.com:org/repo.git", Branch: "main"}},
		Projects: []config.Project{
			{Name: "app1", Repo: "myrepo", Targets: []string{"h1", "h2"}},
			{Name: "app2", Repo: "myrepo", Targets: []string{"h1"}},
		},
	}

	mirrorHead := func(_, _ string) (string, bool) { return "sha1", true }

	var events []planner.RepoEvent
	engine.ReconcileAdditions(context.Background(), oldConfig, newConfig, makeTestStore(t), mirrorHead,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch for same-repo targets, got %d", len(events))
	}
	if len(events[0].ForcedTargets) != 3 {
		t.Errorf("ForcedTargets len = %d, want 3 (app1/h1, app1/h2, app2/h1)", len(events[0].ForcedTargets))
	}
}

// ReconcileProjectChanges

func TestReconcileProjectChanges_NoChanges_NoDeploy(t *testing.T) {
	deployConfig := &config.Config{
		Hosts:    reconcileTestHosts(),
		Repos:    []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}, ExtraFiles: []string{"config"}}},
	}
	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "abc123")

	var dispatched int
	engine.ReconcileProjectChanges(context.Background(), deployConfig, deployConfig, store,
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ },
		slog.Default())
	if dispatched != 0 {
		t.Errorf("expected 0 dispatches for identical configs, got %d", dispatched)
	}
}

func TestReconcileProjectChanges_ExtraFilesChanged_Redeploys(t *testing.T) {
	hosts := reconcileTestHosts()
	oldConfig := &config.Config{
		Hosts:    hosts,
		Repos:    []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}
	newConfig := &config.Config{
		Hosts: hosts,
		Repos: oldConfig.Repos,
		Projects: []config.Project{
			{Name: "app", Repo: "myrepo", Targets: []string{"h1"}, ExtraFiles: []string{"config"}},
		},
	}

	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "abc123")

	var events []planner.RepoEvent
	engine.ReconcileProjectChanges(context.Background(), oldConfig, newConfig, store,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(events))
	}
	if events[0].CommitSha != "abc123" {
		t.Errorf("sha = %q, want abc123", events[0].CommitSha)
	}
	if len(events[0].ForcedTargets) != 1 || events[0].ForcedTargets[0].Host.Name != "h1" {
		t.Errorf("ForcedTargets = %+v, want [{h1}]", events[0].ForcedTargets)
	}
	if events[0].ForcedTargets[0].Project.ExtraFiles[0] != "config" {
		t.Errorf("ForcedTargets uses old project config, want new (with config extra file)")
	}
}

func TestReconcileProjectChanges_ComposeFilesChanged_Redeploys(t *testing.T) {
	hosts := reconcileTestHosts()
	oldConfig := &config.Config{
		Hosts: hosts, Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}, ComposeFiles: []string{"compose.yml"}}},
	}
	newConfig := &config.Config{
		Hosts: hosts, Repos: oldConfig.Repos,
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}, ComposeFiles: []string{"compose.prod.yml"}}},
	}

	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "abc123")

	var events []planner.RepoEvent
	engine.ReconcileProjectChanges(context.Background(), oldConfig, newConfig, store,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(events))
	}
}

func TestReconcileProjectChanges_OnlyWatchPathsChanged_NoDeploy(t *testing.T) {
	hosts := reconcileTestHosts()
	oldConfig := &config.Config{
		Hosts: hosts, Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}, WatchPaths: []string{"src/**"}}},
	}
	newConfig := &config.Config{
		Hosts: hosts, Repos: oldConfig.Repos,
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}, WatchPaths: []string{"src/**", "config/**"}}},
	}

	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "abc123")

	var dispatched int
	engine.ReconcileProjectChanges(context.Background(), oldConfig, newConfig, store,
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ },
		slog.Default())
	if dispatched != 0 {
		t.Errorf("expected 0 dispatches for watch_paths-only change, got %d", dispatched)
	}
}

// Any deploy-affecting host field changing (where/how the deploy runs) redeploys
// the projects on that host, even when the project config itself is untouched.
func TestReconcileProjectChanges_HostFieldChanged_Redeploys(t *testing.T) {
	newHosts := map[string]config.Host{
		"address":    {Name: "h1", Address: "10.0.0.9", DeployDir: "/srv"},
		"deploy_dir": {Name: "h1", Address: "10.0.0.1", DeployDir: "/opt/apps"},
		"user":       {Name: "h1", Address: "10.0.0.1", DeployDir: "/srv", User: "root"},
		"port":       {Name: "h1", Address: "10.0.0.1", DeployDir: "/srv", Port: 2222},
	}
	for name, newHost := range newHosts {
		t.Run(name, func(t *testing.T) {
			oldConfig := &config.Config{
				Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
				Repos:    []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
				Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
			}
			newConfig := &config.Config{
				Hosts:    []config.Host{newHost},
				Repos:    oldConfig.Repos,
				Projects: oldConfig.Projects, // project itself unchanged
			}
			store := makeTestStore(t)
			seedRepoState(t, store, "myrepo", "main", "abc123")

			var events []planner.RepoEvent
			engine.ReconcileProjectChanges(context.Background(), oldConfig, newConfig, store,
				func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
				slog.Default())

			if len(events) != 1 {
				t.Fatalf("expected 1 dispatch for %s change, got %d", name, len(events))
			}
			if !events[0].Redeploy {
				t.Error("host-change redeploy must set Redeploy to bypass same-sha dedup")
			}
			if len(events[0].ForcedTargets) != 1 || events[0].ForcedTargets[0].Host != newHost {
				t.Errorf("ForcedTargets should carry the new host, got %+v", events[0].ForcedTargets)
			}
		})
	}
}

// Only projects on the host that changed are redeployed; projects on unchanged
// hosts are left alone.
func TestReconcileProjectChanges_OnlyChangedHostTargetsRedeploy(t *testing.T) {
	oldConfig := &config.Config{
		Hosts: []config.Host{
			{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"},
			{Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"},
		},
		Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{
			{Name: "appA", Repo: "myrepo", Targets: []string{"h1"}},
			{Name: "appB", Repo: "myrepo", Targets: []string{"h2"}},
		},
	}
	newConfig := &config.Config{
		Hosts: []config.Host{
			{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"},      // unchanged
			{Name: "h2", Address: "10.0.0.2", DeployDir: "/opt/apps"}, // deploy_dir changed
		},
		Repos:    oldConfig.Repos,
		Projects: oldConfig.Projects,
	}
	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "abc123")

	var redeployed []string
	engine.ReconcileProjectChanges(context.Background(), oldConfig, newConfig, store,
		func(_ context.Context, ev planner.RepoEvent) {
			for _, target := range ev.ForcedTargets {
				redeployed = append(redeployed, target.Project.Name)
			}
		},
		slog.Default())

	if len(redeployed) != 1 || redeployed[0] != "appB" {
		t.Errorf("only appB (on changed host h2) should redeploy, got %v", redeployed)
	}
}

func TestReconcileProjectChanges_NewProjectNotInOld_NoDeploy(t *testing.T) {
	// New projects (not in old config) are handled by ReconcileAdditions, not here.
	hosts := reconcileTestHosts()
	oldConfig := &config.Config{Hosts: hosts, Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}}}
	newConfig := &config.Config{
		Hosts: hosts, Repos: oldConfig.Repos,
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}, ExtraFiles: []string{"config"}}},
	}

	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "abc123")

	var dispatched int
	engine.ReconcileProjectChanges(context.Background(), oldConfig, newConfig, store,
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ },
		slog.Default())
	if dispatched != 0 {
		t.Errorf("expected 0 dispatches for brand-new project, got %d", dispatched)
	}
}

func TestReconcileProjectChanges_NewTargetNotRedeployed_OnlyExistingTargets(t *testing.T) {
	// New target plus an extra_files change: additions handle the new target, project-changes the existing ones.
	hosts := []config.Host{
		{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"},
		{Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"},
	}
	oldConfig := &config.Config{
		Hosts: hosts, Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}
	newConfig := &config.Config{
		Hosts: hosts, Repos: oldConfig.Repos,
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1", "h2"}, ExtraFiles: []string{"config"}}},
	}

	store := makeTestStore(t)
	seedRepoState(t, store, "myrepo", "main", "abc123")

	var events []planner.RepoEvent
	engine.ReconcileProjectChanges(context.Background(), oldConfig, newConfig, store,
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) },
		slog.Default())

	if len(events) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(events))
	}
	// Only h1 (existing target) should be in ForcedTargets; h2 is new and handled by ReconcileAdditions.
	if len(events[0].ForcedTargets) != 1 || events[0].ForcedTargets[0].Host.Name != "h1" {
		t.Errorf("ForcedTargets = %+v, want only h1", events[0].ForcedTargets)
	}
}

func TestReconcileProjectChanges_NilStore_Noop(t *testing.T) {
	hosts := reconcileTestHosts()
	oldConfig := &config.Config{
		Hosts: hosts, Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}}},
	}
	newConfig := &config.Config{
		Hosts: hosts, Repos: oldConfig.Repos,
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1"}, ExtraFiles: []string{"config"}}},
	}

	var dispatched int
	engine.ReconcileProjectChanges(context.Background(), oldConfig, newConfig, nil,
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ },
		slog.Default())
	if dispatched != 0 {
		t.Errorf("expected 0 dispatches with nil store, got %d", dispatched)
	}
}

// helpers

func reconcileTestHosts() []config.Host {
	return []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}}
}

func reconcileTestConfig() *config.Config {
	return &config.Config{
		Hosts:    reconcileTestHosts(),
		Projects: []config.Project{{Name: "app", Targets: []string{"h1"}, DeploySubdir: "app"}},
	}
}
