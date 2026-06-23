package engine_test

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/engine"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/reporter"
	"github.com/jancernik/deeplo/internal/runner"
	"github.com/jancernik/deeplo/internal/ssh"
	"github.com/jancernik/deeplo/internal/state"
	"github.com/jancernik/deeplo/internal/webhook"
)

// repoFullName

func TestRepoFullName(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "https url with .git suffix",
			url:  "https://github.com/owner/myrepo.git",
			want: "owner/myrepo",
		},
		{
			name: "ssh scp style",
			url:  "git@github.com:owner/myrepo.git",
			want: "owner/myrepo",
		},
		{
			name: "https url without .git suffix",
			url:  "https://github.com/owner/myrepo",
			want: "owner/myrepo",
		},
		{
			name: "ssh without .git suffix",
			url:  "git@github.com:owner/myrepo",
			want: "owner/myrepo",
		},
		{
			name: "deep path returns everything after github.com/",
			url:  "https://github.com/org/sub/repo.git",
			want: "org/sub/repo",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := engine.RepoFullName(testCase.url)
			if got != testCase.want {
				t.Errorf("repoFullName(%q) = %q, want %q", testCase.url, got, testCase.want)
			}
		})
	}
}

// buildRepoFullNameIndex

func TestBuildRepoFullNameIndex(t *testing.T) {
	repos := []config.RepoConfig{
		{Name: "myapp", URL: "git@github.com:owner/myapp.git", Branch: "main"},
		{Name: "infra", URL: "https://github.com/owner/infra.git", Branch: "main"},
	}
	idx := engine.BuildRepoFullNameIndex(repos)

	if r, ok := idx["owner/myapp"]; !ok || r.Name != "myapp" {
		t.Errorf("expected idx[owner/myapp] = myapp, got %+v (ok=%v)", r, ok)
	}
	if r, ok := idx["owner/infra"]; !ok || r.Name != "infra" {
		t.Errorf("expected idx[owner/infra] = infra, got %+v (ok=%v)", r, ok)
	}
}

// The handler picks up repos added by a config reload without a restart.
func TestWebhookPushHandler_RepoIndexRebuildsOnConfigChange(t *testing.T) {
	var configPtr atomic.Pointer[config.Config]
	configPtr.Store(&config.Config{})
	getConfig := func() *config.Config { return configPtr.Load() }

	var dispatched int
	h := engine.MakeWebhookPushHandler(getConfig, nil,
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ },
		slog.Default(),
	)

	push := webhook.PushEvent{RepoFullName: "owner/newrepo", Branch: "main", CommitSha: "abc"}

	h(context.Background(), push)
	if dispatched != 0 {
		t.Fatalf("expected 0 dispatches before config reload, got %d", dispatched)
	}

	configPtr.Store(&config.Config{
		Repos: []config.RepoConfig{
			{Name: "newrepo", URL: "git@github.com:owner/newrepo.git", Branch: "main", TriggerMode: config.TriggerModeWebhook},
		},
	})
	h(context.Background(), push)
	if dispatched != 1 {
		t.Errorf("expected 1 dispatch after config reload, got %d", dispatched)
	}
}

func TestWebhookPushHandler_RespectsTriggerMode(t *testing.T) {
	cases := []struct {
		name      string
		mode      config.TriggerMode
		wantCalls int
	}{
		{name: "webhook mode dispatches", mode: config.TriggerModeWebhook, wantCalls: 1},
		{name: "poll mode ignores webhook", mode: config.TriggerModePoll, wantCalls: 0},
		{name: "hybrid mode dispatches", mode: config.TriggerModeHybrid, wantCalls: 1},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			deployConfig := &config.Config{
				Repos: []config.RepoConfig{
					{Name: "myapp", URL: "git@github.com:owner/myapp.git", Branch: "main", TriggerMode: testCase.mode},
				},
			}
			var calls int
			h := engine.MakeWebhookPushHandler(
				func() *config.Config { return deployConfig },
				makeTestStore(t),
				func(_ context.Context, _ planner.RepoEvent) { calls++ },
				slog.Default(),
			)

			h(context.Background(), webhook.PushEvent{
				RepoFullName: "owner/myapp",
				Branch:       "main",
				CommitSha:    "deadbeef",
				ChangedFiles: []string{"apps/myapp/main.go"},
			})

			if calls != testCase.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, testCase.wantCalls)
			}
		})
	}
}

func TestWebhookPushHandler_FirstSeenCommitTreatsDiffAsUnknown(t *testing.T) {
	deployConfig := &config.Config{
		Repos: []config.RepoConfig{
			{Name: "myapp", URL: "git@github.com:owner/myapp.git", Branch: "main", TriggerMode: config.TriggerModeWebhook},
		},
	}
	store := makeTestStore(t)

	var got planner.RepoEvent
	h := engine.MakeWebhookPushHandler(
		func() *config.Config { return deployConfig },
		store,
		func(_ context.Context, ev planner.RepoEvent) { got = ev },
		slog.Default(),
	)

	h(context.Background(), webhook.PushEvent{
		RepoFullName: "owner/myapp",
		Branch:       "main",
		CommitSha:    "deadbeef",
		DeliveryID:   "d-1",
		ChangedFiles: []string{"apps/myapp/only-this-file.txt"},
	})

	if got.ChangedFiles != nil {
		t.Fatalf("ChangedFiles = %v, want nil for first-seen webhook commit", got.ChangedFiles)
	}

	repoState, err := store.GetRepoState("myapp", "main")
	if err != nil {
		t.Fatalf("GetRepoState: %v", err)
	}
	if repoState == nil || repoState.LastSeenSha != "deadbeef" {
		t.Fatalf("repo state = %+v, want LastSeenSha deadbeef", repoState)
	}
	if repoState.LastDeployedSha != "deadbeef" {
		t.Fatalf("LastDeployedSha = %q, want deadbeef", repoState.LastDeployedSha)
	}
	if repoState.LastDeliveryID != "d-1" {
		t.Fatalf("LastDeliveryID = %q, want d-1", repoState.LastDeliveryID)
	}
}

func TestWebhookPushHandler_KnownRepoKeepsWebhookChangedFiles(t *testing.T) {
	deployConfig := &config.Config{
		Repos: []config.RepoConfig{
			{Name: "myapp", URL: "git@github.com:owner/myapp.git", Branch: "main", TriggerMode: config.TriggerModeWebhook},
		},
	}
	store := makeTestStore(t)
	if err := store.SaveRepoState(&state.RepoState{
		Repo:            "myapp",
		Branch:          "main",
		LastSeenSha:     "cafebabe",
		LastDeployedSha: "cafebabe",
	}); err != nil {
		t.Fatalf("SaveRepoState: %v", err)
	}

	var got planner.RepoEvent
	h := engine.MakeWebhookPushHandler(
		func() *config.Config { return deployConfig },
		store,
		func(_ context.Context, ev planner.RepoEvent) { got = ev },
		slog.Default(),
	)

	h(context.Background(), webhook.PushEvent{
		RepoFullName: "owner/myapp",
		Branch:       "main",
		CommitSha:    "deadbeef",
		ChangedFiles: []string{"apps/myapp/only-this-file.txt"},
	})

	if len(got.ChangedFiles) != 1 || got.ChangedFiles[0] != "apps/myapp/only-this-file.txt" {
		t.Fatalf("ChangedFiles = %v, want webhook payload paths", got.ChangedFiles)
	}

	repoState, err := store.GetRepoState("myapp", "main")
	if err != nil {
		t.Fatalf("GetRepoState: %v", err)
	}
	if repoState == nil || repoState.LastDeployedSha != "deadbeef" {
		t.Fatalf("LastDeployedSha = %q, want deadbeef", repoState.LastDeployedSha)
	}
}

// State with LastSeenSha set but empty LastDeployedSha is treated as first-seen,
// discarding the webhook diff.
func TestWebhookPushHandler_LastSeenOnlyIsFirstSeen(t *testing.T) {
	deployConfig := &config.Config{
		Repos: []config.RepoConfig{
			{Name: "myapp", URL: "git@github.com:owner/myapp.git", Branch: "main", TriggerMode: config.TriggerModeWebhook},
		},
	}
	store := makeTestStore(t)
	if err := store.SaveRepoState(&state.RepoState{
		Repo:        "myapp",
		Branch:      "main",
		LastSeenSha: "cafebabe",
	}); err != nil {
		t.Fatalf("SaveRepoState: %v", err)
	}

	var got planner.RepoEvent
	h := engine.MakeWebhookPushHandler(
		func() *config.Config { return deployConfig },
		store,
		func(_ context.Context, ev planner.RepoEvent) { got = ev },
		slog.Default(),
	)

	h(context.Background(), webhook.PushEvent{
		RepoFullName: "owner/myapp",
		Branch:       "main",
		CommitSha:    "deadbeef",
		ChangedFiles: []string{"apps/myapp/only-this-file.txt"},
	})

	if got.ChangedFiles != nil {
		t.Fatalf("ChangedFiles = %v, want nil - no prior deploy means firstSeen=true", got.ChangedFiles)
	}
}

// A webhook push must record LastDeployedSha so the poller does not redeploy the
// same commit, keeping the poll and webhook paths symmetric.
func TestWebhookPushHandler_SetsLastDeployedSha(t *testing.T) {
	deployConfig := &config.Config{
		Repos: []config.RepoConfig{
			{Name: "myapp", URL: "git@github.com:owner/myapp.git", Branch: "main", TriggerMode: config.TriggerModeHybrid},
		},
	}
	store := makeTestStore(t)

	h := engine.MakeWebhookPushHandler(
		func() *config.Config { return deployConfig },
		store,
		func(_ context.Context, _ planner.RepoEvent) {},
		slog.Default(),
	)
	h(context.Background(), webhook.PushEvent{
		RepoFullName: "owner/myapp",
		Branch:       "main",
		CommitSha:    "aabbccdd",
	})

	repoState, err := store.GetRepoState("myapp", "main")
	if err != nil {
		t.Fatalf("GetRepoState: %v", err)
	}
	if repoState == nil {
		t.Fatal("expected repo state to be saved")
	}
	if repoState.LastDeployedSha != "aabbccdd" {
		t.Errorf("LastDeployedSha = %q, want aabbccdd", repoState.LastDeployedSha)
	}
	if repoState.LastSeenSha != "aabbccdd" {
		t.Errorf("LastSeenSha = %q, want aabbccdd", repoState.LastSeenSha)
	}
}

// pushHandler dedup vs forced targets

// waitForNewDeployment polls until the latest deployment for (project, host)
// differs from excludeID, or times out (returning nil).
func waitForNewDeployment(t *testing.T, store *state.FileStore, project, host, excludeID string) *state.Deployment {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		latest, err := store.GetLatestDeployment(project, host)
		if err != nil {
			t.Fatalf("GetLatestDeployment: %v", err)
		}
		if latest != nil && latest.ID != excludeID {
			return latest
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func dispatchHandlerForTest(t *testing.T, store *state.FileStore, event planner.RepoEvent, deployConfig *config.Config) {
	t.Helper()
	bc := &bootstrap.Config{DataPath: t.TempDir()}
	jobRunner := runner.New(runner.Config{MaxWorkers: 1, MaxHostConcurrency: 1}, slog.Default())
	jobRunner.Start()
	go engine.DrainResults(jobRunner.Results())
	t.Cleanup(jobRunner.Stop)

	handler := engine.MakePushHandler(
		func() *config.Config { return deployConfig },
		bc, jobRunner, ssh.NewDialer(), store, reporter.Noop(), nil, slog.Default(),
	)
	handler(context.Background(), event)
}

func seededTarget() (config.Project, config.Host, planner.DeployTarget, *config.Config) {
	project := config.Project{Name: "app", Repo: "r1", ComposeFiles: []string{"docker-compose.yml"}, Targets: []string{"h1"}}
	host := config.Host{Name: "h1", Address: "127.0.0.1", DeployDir: "/tmp"}
	target := planner.DeployTarget{
		Project: project,
		Host:    host,
		Repo:    config.RepoConfig{Name: "r1", URL: "file:///nonexistent/repo.git", Branch: "main"},
	}
	// Target must still be in config; reconcile only forces existing targets.
	deployConfig := &config.Config{Hosts: []config.Host{host}, Projects: []config.Project{project}}
	return project, host, target, deployConfig
}

// Redeploy=true (config change at an unchanged commit) bypasses dedup even
// though the store holds a success at the same SHA.
func TestPushHandler_RedeployBypassesDedup(t *testing.T) {
	store := makeTestStore(t)
	now := time.Now().UTC()
	if err := store.SaveDeployment(&state.Deployment{
		ID: "seed", Project: "app", Host: "h1",
		CommitSha: "abc", Status: state.StatusSuccess,
		StartedAt: now, CompletedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	_, _, target, deployConfig := seededTarget()
	dispatchHandlerForTest(t, store, planner.RepoEvent{
		Source:        planner.TriggerReconcileProjectChange,
		RepoName:      "r1",
		Branch:        "main",
		CommitSha:     "abc",
		ForcedTargets: []planner.DeployTarget{target},
		Redeploy:      true,
	}, deployConfig)

	// A new deployment record (the attempt, failing at git fetch) proves it wasn't deduped.
	if got := waitForNewDeployment(t, store, "app", "h1", "seed"); got == nil {
		t.Fatal("Redeploy event was deduped: expected a new deployment attempt, got none")
	}
}

// A forced target without Redeploy, already deployed at the same SHA, is deduped,
// keeping startup resume idempotent and safe to overlap with reconcile-additions.
func TestPushHandler_ForcedTargetWithoutRedeployIsDeduped(t *testing.T) {
	store := makeTestStore(t)
	now := time.Now().UTC()
	if err := store.SaveDeployment(&state.Deployment{
		ID: "seed", Project: "app", Host: "h1",
		CommitSha: "abc", Status: state.StatusSuccess,
		StartedAt: now, CompletedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	_, _, target, deployConfig := seededTarget()
	dispatchHandlerForTest(t, store, planner.RepoEvent{
		Source:        planner.TriggerResume,
		RepoName:      "r1",
		Branch:        "main",
		CommitSha:     "abc",
		ForcedTargets: []planner.DeployTarget{target},
	}, deployConfig)

	if got := waitForNewDeployment(t, store, "app", "h1", "seed"); got != nil {
		t.Fatalf("forced-without-redeploy at an already-deployed SHA should be deduped, got %s", got.ID)
	}
}

// A forced target removed from config since it was queued must be skipped; the
// level-trigger check prevents resurrecting a deleted target.
func TestPushHandler_ForcedTargetNotInConfigIsSkipped(t *testing.T) {
	store := makeTestStore(t)

	project := config.Project{Name: "app", Repo: "r1", ComposeFiles: []string{"docker-compose.yml"}, Targets: []string{"h1"}}
	host := config.Host{Name: "h1", Address: "127.0.0.1", DeployDir: "/tmp"}
	target := planner.DeployTarget{
		Project: project,
		Host:    host,
		Repo:    config.RepoConfig{Name: "r1", URL: "file:///nonexistent/repo.git", Branch: "main"},
	}

	dispatchHandlerForTest(t, store, planner.RepoEvent{
		Source:        planner.TriggerReconcileAddition,
		RepoName:      "r1",
		Branch:        "main",
		CommitSha:     "abc",
		ForcedTargets: []planner.DeployTarget{target},
	}, &config.Config{})

	if got := waitForNewDeployment(t, store, "app", "h1", ""); got != nil {
		t.Fatalf("expected no deploy for a target absent from config, got %v", got.ID)
	}
}

// A non-forced event for an already-succeeded SHA is still deduped; only forced
// reconcile targets bypass it.
func TestPushHandler_NonForcedSameShaIsDeduped(t *testing.T) {
	store := makeTestStore(t)
	now := time.Now().UTC()
	if err := store.SaveDeployment(&state.Deployment{
		ID: "seed", Project: "app", Host: "h1",
		CommitSha: "abc", Status: state.StatusSuccess,
		StartedAt: now, CompletedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	deployConfig := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "127.0.0.1", DeployDir: "/tmp"}},
		Repos:    []config.RepoConfig{{Name: "r1", URL: "file:///nonexistent/repo.git", Branch: "main"}},
		Projects: []config.Project{{Name: "app", Repo: "r1", ComposeFiles: []string{"docker-compose.yml"}, Targets: []string{"h1"}}},
	}

	dispatchHandlerForTest(t, store, planner.RepoEvent{
		Source:    planner.TriggerWebhook,
		RepoName:  "r1",
		Branch:    "main",
		CommitSha: "abc",
	}, deployConfig)

	if got := waitForNewDeployment(t, store, "app", "h1", "seed"); got != nil {
		t.Fatalf("non-forced same-SHA event should be deduped, but a new deployment %q was created", got.ID)
	}
}
