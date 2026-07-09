package poller

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/state"
)

type fakeRepo struct {
	objects    map[string]bool
	diff       []string
	diffByBase map[string][]string // per-base diff; keyed by oldSha, overrides diff when set
	diffErr    error
	ensureErr  error
	ancestorOf map[string]bool // sha → true means sha is an ancestor of the keyed descendant
}

func (f *fakeRepo) HasCommit(_ context.Context, sha string) bool { return f.objects[sha] }
func (f *fakeRepo) EnsureCommit(_ context.Context, sha string) error {
	if f.ensureErr != nil {
		return f.ensureErr
	}
	if f.objects == nil {
		f.objects = make(map[string]bool)
	}
	f.objects[sha] = true
	return nil
}
func (f *fakeRepo) DiffFiles(_ context.Context, oldSha, _ string) ([]string, error) {
	if f.diffByBase != nil {
		return f.diffByBase[oldSha], f.diffErr
	}
	return f.diff, f.diffErr
}
func (f *fakeRepo) IsAncestor(_ context.Context, ancestor, _ string) bool {
	return f.ancestorOf[ancestor]
}

func makeStore(t *testing.T) *state.FileStore {
	t.Helper()
	s := state.NewFileStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init state: %v", err)
	}
	return s
}

func makeConfig(mode config.TriggerMode, interval time.Duration) *config.Config {
	return &config.Config{
		Hosts: []config.Host{{Name: "h1", Address: "1.2.3.4", DeployDir: "/srv"}},
		Repos: []config.RepoConfig{{
			Name:         "myrepo",
			URL:          "git@github.com:owner/myapp.git",
			Branch:       "main",
			TriggerMode:  mode,
			PollInterval: interval,
		}},
		Projects: []config.Project{{
			Name:         "myapp",
			Repo:         "myrepo",
			RepoSubdir:   "apps/myapp",
			ComposeFiles: []string{"compose.yaml"},
			Targets:      []string{"h1"},
		}},
	}
}

func newPoller(deployConfig *config.Config, store *state.FileStore) (*Poller, <-chan planner.RepoEvent) {
	ch := make(chan planner.RepoEvent, 32)
	p := New(deployConfig, func() *config.Config { return deployConfig }, "", "", store,
		func(_ context.Context, ev planner.RepoEvent) { ch <- ev },
		nil,
		slog.Default(), nil)
	return p, ch
}

func withFakeRepo(p *Poller, repo repoOpener) {
	p.findRepo = func(_ string) (repoOpener, error) { return repo, nil }
}

func withNoMirror(p *Poller) {
	p.findRepo = func(_ string) (repoOpener, error) { return nil, nil }
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

func forcedProjects(ev planner.RepoEvent) []string {
	var names []string
	for _, target := range ev.ForcedTargets {
		names = append(names, target.Project.Name)
	}
	return names
}

// HandleSHA

// A never-deployed target is dispatched (bootstrap) on the first commit the
// poller sees, as forced targets rather than a changed-files list.
func TestHandleSHA_NewCommit_Dispatches(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModePoll, time.Minute)
	p, ch := newPoller(deployConfig, makeStore(t))
	withNoMirror(p)

	p.HandleSHA(context.Background(), deployConfig.Repos[0], "aabbccdd1122334455667788990011223344556677")

	select {
	case ev := <-ch:
		if ev.Source != planner.TriggerPoll {
			t.Errorf("source = %q, want poll", ev.Source)
		}
		if ev.CommitSha != "aabbccdd1122334455667788990011223344556677" {
			t.Errorf("sha = %q", ev.CommitSha)
		}
		if ev.Branch != "main" || ev.RepoName != "myrepo" {
			t.Errorf("branch/repo = %q/%q, want main/myrepo", ev.Branch, ev.RepoName)
		}
		if got := forcedProjects(ev); len(got) != 1 || got[0] != "myapp" {
			t.Errorf("ForcedTargets projects = %v, want [myapp]", got)
		}
	default:
		t.Fatal("no event dispatched")
	}
}

// A config-repo reload failure defers the commit: no deploy is dispatched and
// LastDeployedSha is left unchanged, so the next poll retries it.
func TestHandleSHA_ReloadFailureDefersDeploy(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModePoll, time.Minute)
	store := makeStore(t)
	ch := make(chan planner.RepoEvent, 1)
	p := New(deployConfig, func() *config.Config { return deployConfig }, "", "", store,
		func(_ context.Context, ev planner.RepoEvent) { ch <- ev },
		func(_ context.Context, _ string) error { return errors.New("config unavailable") },
		slog.Default(), nil)
	withNoMirror(p)

	const sha = "aabbccdd1122334455667788990011223344556677"
	p.HandleSHA(context.Background(), deployConfig.Repos[0], sha)

	select {
	case <-ch:
		t.Fatal("deploy dispatched despite reload failure")
	default:
	}
	repoState, err := store.GetRepoState("myrepo", "main")
	if err != nil {
		t.Fatalf("GetRepoState: %v", err)
	}
	if repoState != nil && repoState.LastDeployedSha == sha {
		t.Error("LastDeployedSha must not advance when the reload fails")
	}
}

// A commit deferred by a reload failure is dispatched on the next poll once the
// config recovers, since the first attempt left LastDeployedSha unchanged.
func TestHandleSHA_DeferredCommitRetriesAfterReloadRecovers(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModePoll, time.Minute)
	store := makeStore(t)
	ch := make(chan planner.RepoEvent, 1)
	reloadOK := false
	p := New(deployConfig, func() *config.Config { return deployConfig }, "", "", store,
		func(_ context.Context, ev planner.RepoEvent) { ch <- ev },
		func(_ context.Context, _ string) error {
			if !reloadOK {
				return errors.New("config unavailable")
			}
			return nil
		},
		slog.Default(), nil)
	withNoMirror(p)

	const sha = "aabbccdd1122334455667788990011223344556677"

	p.HandleSHA(context.Background(), deployConfig.Repos[0], sha) // reload broken: deferred
	select {
	case <-ch:
		t.Fatal("deploy dispatched despite reload failure")
	default:
	}

	reloadOK = true
	p.HandleSHA(context.Background(), deployConfig.Repos[0], sha) // recovered: same commit deploys
	select {
	case ev := <-ch:
		if ev.CommitSha != sha {
			t.Errorf("dispatched sha = %q, want %q", ev.CommitSha, sha)
		}
	default:
		t.Fatal("deploy not dispatched after config recovered")
	}
}

// Cross-trigger invariant: if a webhook already recorded LastDeployedSha, the
// poller must not redeploy that same commit.
func TestHandleSHA_WebhookDeployedSHA_NoDispatch(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModeHybrid, time.Minute)
	store := makeStore(t)
	sha := "aabbccdd1122334455667788990011223344556677"

	// Simulate a prior webhook deploy having written LastDeployedSha.
	if err := store.SaveRepoState(&state.RepoState{
		Repo:            "myrepo",
		Branch:          "main",
		LastSeenSha:     sha,
		LastDeployedSha: sha,
		TriggerSource:   "webhook",
	}); err != nil {
		t.Fatal(err)
	}

	p, ch := newPoller(deployConfig, store)
	withNoMirror(p)
	p.HandleSHA(context.Background(), deployConfig.Repos[0], sha)

	select {
	case ev := <-ch:
		t.Errorf("poller should not redeploy a SHA already recorded by webhook, got event %+v", ev)
	default:
	}
}

func TestHandleSHA_SameCommit_NoDispatch(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModePoll, time.Minute)
	store := makeStore(t)
	sha := "aabbccdd1122334455667788990011223344556677"
	if err := store.SaveRepoState(&state.RepoState{
		Repo: "myrepo", Branch: "main", LastDeployedSha: sha,
	}); err != nil {
		t.Fatal(err)
	}

	p, ch := newPoller(deployConfig, store)
	withNoMirror(p)
	p.HandleSHA(context.Background(), deployConfig.Repos[0], sha)

	select {
	case ev := <-ch:
		t.Errorf("expected no event for same SHA, got %+v", ev)
	default:
	}
}

// A target behind head whose files changed since its own last deploy is
// dispatched; a target already at head is not.
func TestHandleSHA_TargetBehindAndTouched_Dispatches(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModePoll, time.Minute)
	store := makeStore(t)
	oldSHA := "0000000000000000000000000000000000000001"
	newSHA := "aabbccdd1122334455667788990011223344556677"
	seedSuccess(t, store, "myapp", "h1", oldSHA)
	if err := store.SaveRepoState(&state.RepoState{
		Repo: "myrepo", Branch: "main", LastDeployedSha: oldSHA,
	}); err != nil {
		t.Fatal(err)
	}

	p, ch := newPoller(deployConfig, store)
	withFakeRepo(p, &fakeRepo{
		objects:    map[string]bool{oldSHA: true, newSHA: true},
		ancestorOf: map[string]bool{oldSHA: true},
		diff:       []string{"apps/myapp/compose.yaml"},
	})

	p.HandleSHA(context.Background(), deployConfig.Repos[0], newSHA)

	select {
	case ev := <-ch:
		if got := forcedProjects(ev); len(got) != 1 || got[0] != "myapp" {
			t.Errorf("ForcedTargets projects = %v, want [myapp]", got)
		}
	default:
		t.Fatal("no event dispatched")
	}
}

// A target whose files were untouched between its last deploy and head is not
// redeployed, even though the repo saw a new commit.
func TestHandleSHA_TargetUntouched_NoDispatch(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModePoll, time.Minute)
	store := makeStore(t)
	oldSHA := "0000000000000000000000000000000000000001"
	newSHA := "aabbccdd1122334455667788990011223344556677"
	seedSuccess(t, store, "myapp", "h1", oldSHA)
	if err := store.SaveRepoState(&state.RepoState{
		Repo: "myrepo", Branch: "main", LastDeployedSha: oldSHA,
	}); err != nil {
		t.Fatal(err)
	}

	p, ch := newPoller(deployConfig, store)
	withFakeRepo(p, &fakeRepo{
		objects:    map[string]bool{oldSHA: true, newSHA: true},
		ancestorOf: map[string]bool{oldSHA: true},
		diff:       []string{"apps/other/compose.yaml"}, // does not touch apps/myapp
	})

	p.HandleSHA(context.Background(), deployConfig.Repos[0], newSHA)

	select {
	case ev := <-ch:
		t.Errorf("untouched target should not deploy, got %v", forcedProjects(ev))
	default:
	}
}

// The core of gap #1: a target's last deploy (W) lags a LastDeployedSha (Y) that
// advanced past a commit touching it (a deferral or truncated webhook). The poller
// must diff from the target's own base W — where the change is visible — not from
// the repo-level Y, and so deploy it. This heals stranding on the next commit.
func TestHandleSHA_StrandedTargetHealsOnNextCommit(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModePoll, time.Minute)
	store := makeStore(t)
	const (
		wSHA = "1111111111111111111111111111111111111111" // target's last successful deploy
		ySHA = "2222222222222222222222222222222222222222" // repo-level LastDeployedSha (drifted)
		zSHA = "3333333333333333333333333333333333333333" // new head
	)
	seedSuccess(t, store, "myapp", "h1", wSHA)
	if err := store.SaveRepoState(&state.RepoState{
		Repo: "myrepo", Branch: "main", LastDeployedSha: ySHA,
	}); err != nil {
		t.Fatal(err)
	}

	p, ch := newPoller(deployConfig, store)
	withFakeRepo(p, &fakeRepo{
		objects:    map[string]bool{wSHA: true, ySHA: true, zSHA: true},
		ancestorOf: map[string]bool{wSHA: true, ySHA: true},
		// From the target's base W the change is visible; from the drifted Y it is not.
		diffByBase: map[string][]string{
			wSHA: {"apps/myapp/compose.yaml"},
			ySHA: {"apps/other/compose.yaml"},
		},
	})

	p.HandleSHA(context.Background(), deployConfig.Repos[0], zSHA)

	select {
	case ev := <-ch:
		if got := forcedProjects(ev); len(got) != 1 || got[0] != "myapp" {
			t.Errorf("stranded target should heal; ForcedTargets = %v, want [myapp]", got)
		}
	default:
		t.Fatal("stranded target was not redeployed (diff used the drifted base, not the target's)")
	}
}

// When the diff can't be computed, a behind target is deployed conservatively
// rather than skipped: fetch failure, diff error, and no mirror all fall back to
// deploying it. (A never-deployed target would deploy anyway, so seed a prior
// success to isolate the "can't diff" fallback.)
func TestHandleSHA_DiffUnavailable_DeploysTarget(t *testing.T) {
	oldSHA := "0000000000000000000000000000000000000001"
	newSHA := "aabbccdd1122334455667788990011223344556677"

	cases := map[string]*fakeRepo{
		"fetch fails": {objects: map[string]bool{oldSHA: true}, ensureErr: errors.New("fetch failed")},
		"diff error":  {objects: map[string]bool{oldSHA: true, newSHA: true}, ancestorOf: map[string]bool{oldSHA: true}, diffErr: errors.New("diff failed")},
	}
	for name, fake := range cases {
		t.Run(name, func(t *testing.T) {
			deployConfig := makeConfig(config.TriggerModePoll, time.Minute)
			store := makeStore(t)
			seedSuccess(t, store, "myapp", "h1", oldSHA)
			if err := store.SaveRepoState(&state.RepoState{Repo: "myrepo", Branch: "main", LastDeployedSha: oldSHA}); err != nil {
				t.Fatal(err)
			}
			p, ch := newPoller(deployConfig, store)
			withFakeRepo(p, fake)

			p.HandleSHA(context.Background(), deployConfig.Repos[0], newSHA)

			select {
			case ev := <-ch:
				if got := forcedProjects(ev); len(got) != 1 || got[0] != "myapp" {
					t.Errorf("ForcedTargets = %v, want [myapp]", got)
				}
			default:
				t.Fatal("no event dispatched")
			}
		})
	}

	t.Run("no mirror", func(t *testing.T) {
		deployConfig := makeConfig(config.TriggerModePoll, time.Minute)
		store := makeStore(t)
		seedSuccess(t, store, "myapp", "h1", oldSHA)
		if err := store.SaveRepoState(&state.RepoState{Repo: "myrepo", Branch: "main", LastDeployedSha: oldSHA}); err != nil {
			t.Fatal(err)
		}
		p, ch := newPoller(deployConfig, store)
		withNoMirror(p)

		p.HandleSHA(context.Background(), deployConfig.Repos[0], newSHA)

		select {
		case ev := <-ch:
			if got := forcedProjects(ev); len(got) != 1 || got[0] != "myapp" {
				t.Errorf("ForcedTargets = %v, want [myapp]", got)
			}
		default:
			t.Fatal("no event dispatched")
		}
	})
}

// Hybrid-mode race: after a webhook advances LastDeployedSha, a stale (older)
// ls-remote result must be skipped rather than deploying backwards.
func TestHandleSHA_StalePoll_Skipped(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModeHybrid, time.Minute)
	store := makeStore(t)
	newerSHA := "9999999999999999999999999999999999999999" // webhook already advanced to this
	olderSHA := "1111111111111111111111111111111111111111" // stale git ls-remote result
	if err := store.SaveRepoState(&state.RepoState{
		Repo: "myrepo", Branch: "main", LastDeployedSha: newerSHA,
	}); err != nil {
		t.Fatal(err)
	}

	p, ch := newPoller(deployConfig, store)
	// Mirror has both commits; olderSHA is confirmed to be an ancestor of newerSHA.
	withFakeRepo(p, &fakeRepo{
		objects:    map[string]bool{olderSHA: true, newerSHA: true},
		ancestorOf: map[string]bool{olderSHA: true},
	})

	p.HandleSHA(context.Background(), deployConfig.Repos[0], olderSHA)

	select {
	case ev := <-ch:
		t.Errorf("expected no dispatch for stale poll, got event with sha %s", ev.CommitSha)
	default:
	}

	// LastDeployedSha must not have been rolled back to the older commit.
	repoState, err := store.GetRepoState("myrepo", "main")
	if err != nil || repoState == nil {
		t.Fatal("could not read repo state")
	}
	if repoState.LastDeployedSha != newerSHA {
		t.Errorf("LastDeployedSha = %q, want %q", repoState.LastDeployedSha, newerSHA)
	}
}

// The stale-poll guard also fires when only the config mirror has both commits.
func TestHandleSHA_StalePoll_SkippedViaConfigMirror(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModeHybrid, time.Minute)
	store := makeStore(t)
	newerSHA := "9999999999999999999999999999999999999999"
	olderSHA := "1111111111111111111111111111111111111111"
	if err := store.SaveRepoState(&state.RepoState{
		Repo: "myrepo", Branch: "main", LastDeployedSha: newerSHA,
	}); err != nil {
		t.Fatal(err)
	}

	p, ch := newPoller(deployConfig, store)
	// Deploy mirror has olderSHA but not newerSHA, so it can't check ancestry.
	withFakeRepo(p, &fakeRepo{
		objects: map[string]bool{olderSHA: true},
	})
	// Config mirror has both; olderSHA is an ancestor.
	configMirror := &fakeRepo{
		objects:    map[string]bool{olderSHA: true, newerSHA: true},
		ancestorOf: map[string]bool{olderSHA: true},
	}
	p.findConfigMirror = func(_ string) (repoOpener, error) { return configMirror, nil }

	p.HandleSHA(context.Background(), deployConfig.Repos[0], olderSHA)

	select {
	case ev := <-ch:
		t.Errorf("expected no dispatch when config mirror confirms stale result, got sha %s", ev.CommitSha)
	default:
	}
}

// Subscriptions

func TestSubscriptions_PollMode_ReturnsEntry(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModePoll, 30*time.Second)
	p, _ := newPoller(deployConfig, makeStore(t))
	subs := p.Subscriptions()
	if len(subs) != 1 {
		t.Fatalf("len(subs) = %d, want 1", len(subs))
	}
	if subs[0].URL != deployConfig.Repos[0].URL {
		t.Errorf("URL = %q, want %q", subs[0].URL, deployConfig.Repos[0].URL)
	}
	if subs[0].Branch != "main" {
		t.Errorf("Branch = %q, want main", subs[0].Branch)
	}
	if subs[0].Interval != 30*time.Second {
		t.Errorf("Interval = %v, want 30s", subs[0].Interval)
	}
	if subs[0].Handler == nil {
		t.Error("Handler is nil")
	}
}

func TestSubscriptions_HybridMode_ReturnsEntry(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModeHybrid, time.Minute)
	p, _ := newPoller(deployConfig, makeStore(t))
	if len(p.Subscriptions()) != 1 {
		t.Errorf("hybrid mode: expected 1 subscription, got %d", len(p.Subscriptions()))
	}
}

func TestSubscriptions_WebhookMode_Empty(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModeWebhook, time.Minute)
	p, _ := newPoller(deployConfig, makeStore(t))
	if len(p.Subscriptions()) != 0 {
		t.Errorf("webhook mode: expected 0 subscriptions, got %d", len(p.Subscriptions()))
	}
}

func TestSubscriptions_DefaultInterval(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModePoll, 0)
	p, _ := newPoller(deployConfig, makeStore(t))
	subs := p.Subscriptions()
	if len(subs) != 1 {
		t.Fatalf("len(subs) = %d, want 1", len(subs))
	}
	if subs[0].Interval != 60*time.Second {
		t.Errorf("Interval = %v, want 60s", subs[0].Interval)
	}
}

func TestSubscriptions_MultipleRepos_OnlyPollAndHybrid(t *testing.T) {
	deployConfig := &config.Config{
		Repos: []config.RepoConfig{
			{Name: "a", URL: "git@github.com:o/a.git", Branch: "main", TriggerMode: config.TriggerModePoll},
			{Name: "b", URL: "git@github.com:o/b.git", Branch: "main", TriggerMode: config.TriggerModeWebhook},
			{Name: "c", URL: "git@github.com:o/c.git", Branch: "main", TriggerMode: config.TriggerModeHybrid},
		},
	}
	p, _ := newPoller(deployConfig, makeStore(t))
	subs := p.Subscriptions()
	if len(subs) != 2 {
		t.Errorf("expected 2 subscriptions (poll + hybrid), got %d", len(subs))
	}
}

func TestSubscriptions_Handler_DispatchesOnNewSHA(t *testing.T) {
	deployConfig := makeConfig(config.TriggerModePoll, time.Minute)
	store := makeStore(t)
	p, ch := newPoller(deployConfig, store)
	withNoMirror(p)

	subs := p.Subscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}

	subs[0].Handler(context.Background(), "newsha123")

	select {
	case ev := <-ch:
		if ev.CommitSha != "newsha123" {
			t.Errorf("CommitSha = %q, want newsha123", ev.CommitSha)
		}
	default:
		t.Fatal("handler did not dispatch event")
	}
}
