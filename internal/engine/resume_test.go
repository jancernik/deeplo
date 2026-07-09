package engine_test

import (
	"context"
	"errors"
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
		Projects: []config.Project{{Name: "app", Repo: "myrepo", Targets: []string{"h1", "h2"}, RepoSubdir: "app", DeploySubdir: "app"}},
	}
}

type stubDiffer struct {
	files         []string
	commitMissing bool // HasCommit returns false until EnsureCommit fetches
	fetched       bool
	notAncestor   bool
	diffErr       error
	ensureErr     error
	ensureCalls   int
}

func (stub *stubDiffer) HasCommit(context.Context, string) bool {
	return !stub.commitMissing || stub.fetched
}

func (stub *stubDiffer) EnsureCommit(context.Context, string) error {
	stub.ensureCalls++
	if stub.ensureErr != nil {
		return stub.ensureErr
	}
	stub.fetched = true
	return nil
}

func (stub *stubDiffer) IsAncestor(context.Context, string, string) bool {
	return !stub.notAncestor
}

func (stub *stubDiffer) DiffFiles(context.Context, string, string) ([]string, error) {
	return stub.files, stub.diffErr
}

var errStub = errors.New("stub diff failure")

// resolver returns a resume mirror-resolver that reports head and the given mirror
// (nil for "no local mirror") for every repo.
func resolver(head string, repo engine.MirrorRepo) func(string, string) (engine.MirrorRepo, string, bool) {
	return func(string, string) (engine.MirrorRepo, string, bool) { return repo, head, true }
}

// noResolver reports no mirror and no head, forcing the repo-state fallback.
func noResolver(string, string) (engine.MirrorRepo, string, bool) { return nil, "", false }

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

func seedFailed(t *testing.T, store *state.FileStore, project, host, sha string) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.SaveDeployment(&state.Deployment{
		ID: project + "-" + host + "-" + sha, Project: project, Host: host,
		CommitSha: sha, Status: state.StatusFailed, StartedAt: now, CompletedAt: &now,
	}); err != nil {
		t.Fatalf("seed failed deployment: %v", err)
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

// Only targets not already deployed at the desired commit are resumed: here h1
// succeeded at the mirror head and must be skipped, while h2 (no record) is
// dispatched. This is the dropped-on-shutdown recovery case.
func TestResume_DeploysOnlyIncompleteTargets(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)
	seedSuccess(t, store, "app", "h1", "headsha")

	var events []planner.RepoEvent
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, resolver("headsha", nil),
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

// A target behind head is resumed when the intervening commits touched its watched paths.
func TestResume_RedeploysTargetBehindHead_WhenTouched(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)
	seedSuccess(t, store, "app", "h1", "oldsha")
	seedSuccess(t, store, "app", "h2", "oldsha")

	differ := &stubDiffer{files: []string{"app/docker-compose.yml"}}

	var events []planner.RepoEvent
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, resolver("newsha", differ),
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) }, slog.Default())

	hosts := collectForced(events)
	if !hosts["h1"] || !hosts["h2"] {
		t.Errorf("both targets were touched between oldsha..newsha and should be resumed, got %v", hosts)
	}
}

// A target behind head whose watched paths were untouched is left alone (the whole-fleet-redeploy guard).
func TestResume_SkipsTargetBehindHead_WhenUntouched(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)
	seedSuccess(t, store, "app", "h1", "oldsha")
	seedSuccess(t, store, "app", "h2", "oldsha")

	differ := &stubDiffer{files: []string{"other-project/config.yml"}}

	dispatched := 0
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, resolver("newsha", differ),
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ }, slog.Default())

	if dispatched != 0 {
		t.Errorf("no target was touched between oldsha..newsha; expected no dispatch, got %d", dispatched)
	}
}

// Within one repo, only the project whose watched paths changed is resumed; a
// sibling project left untouched by the same commit range is skipped. This is the
// per-project granularity the fix exists for.
func TestResume_ResumesOnlyTheTouchedProject(t *testing.T) {
	deployConfig := &config.Config{
		Hosts: []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Repos: []config.RepoConfig{{Name: "myrepo", Branch: "main"}},
		Projects: []config.Project{
			{Name: "web", Repo: "myrepo", Targets: []string{"h1"}, RepoSubdir: "web", DeploySubdir: "web"},
			{Name: "api", Repo: "myrepo", Targets: []string{"h1"}, RepoSubdir: "api", DeploySubdir: "api"},
		},
	}
	store := makeTestStore(t)
	seedSuccess(t, store, "web", "h1", "oldsha")
	seedSuccess(t, store, "api", "h1", "oldsha")

	// The commit range touched only the web project's subtree.
	differ := &stubDiffer{files: []string{"web/compose.yaml"}}

	var events []planner.RepoEvent
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, resolver("newsha", differ),
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) }, slog.Default())

	projects := map[string]bool{}
	for _, ev := range events {
		for _, target := range ev.ForcedTargets {
			projects[target.Project.Name] = true
		}
	}
	if !projects["web"] {
		t.Error("web was touched between oldsha..newsha and should be resumed")
	}
	if projects["api"] {
		t.Error("api was not touched; should not be resumed")
	}
}

// A failed target is retried even when the diff would not match it.
func TestResume_RetriesFailedTargetEvenWhenUntouched(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)
	seedFailed(t, store, "app", "h1", "oldsha")
	seedFailed(t, store, "app", "h2", "oldsha")

	differ := &stubDiffer{files: []string{"other-project/config.yml"}}

	var events []planner.RepoEvent
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, resolver("newsha", differ),
		func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) }, slog.Default())

	hosts := collectForced(events)
	if !hosts["h1"] || !hosts["h2"] {
		t.Errorf("failed targets should be retried regardless of diff, got %v", hosts)
	}
}

// When the mirror can't answer, resume falls back to deploying rather than dropping the target.
func TestResume_FallsBackToUnconditionalWhenMirrorUnavailable(t *testing.T) {
	cases := map[string]func(string, string) (engine.MirrorRepo, string, bool){
		"nil mirror":   resolver("newsha", nil),
		"diff error":   resolver("newsha", &stubDiffer{diffErr: errStub, files: []string{"other/x"}}),
		"not ancestor": resolver("newsha", &stubDiffer{notAncestor: true, files: []string{"other/x"}}),
	}
	for name, resolve := range cases {
		t.Run(name, func(t *testing.T) {
			deployConfig := resumeTestConfig()
			store := makeTestStore(t)
			seedSuccess(t, store, "app", "h1", "oldsha")
			seedSuccess(t, store, "app", "h2", "oldsha")

			var events []planner.RepoEvent
			engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, resolve,
				func(_ context.Context, ev planner.RepoEvent) { events = append(events, ev) }, slog.Default())

			hosts := collectForced(events)
			if !hosts["h1"] || !hosts["h2"] {
				t.Errorf("mirror unavailable should fall back to resuming both, got %v", hosts)
			}
		})
	}
}

// Regression: a head commit that was never deployed is absent from the mirror
// (e.g. a config-only push, or a revert whose tree equals the last deploy). Resume
// must fetch it and diff, not treat "head missing" as "redeploy everything".
func TestResume_FetchesMissingHeadBeforeDiffing(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)
	seedSuccess(t, store, "app", "h1", "oldsha")
	seedSuccess(t, store, "app", "h2", "oldsha")

	// Head is not in the mirror yet; once fetched, the diff touches nothing the
	// project watches.
	differ := &stubDiffer{commitMissing: true, files: []string{"other-project/config.yml"}}

	dispatched := 0
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, resolver("newsha", differ),
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ }, slog.Default())

	if differ.ensureCalls == 0 {
		t.Error("resume must fetch the missing head commit before diffing")
	}
	if dispatched != 0 {
		t.Errorf("head was fetched and the diff touched nothing; expected no dispatch, got %d", dispatched)
	}
}

// When every target is already at the desired commit, nothing is dispatched.
func TestResume_NothingWhenAllUpToDate(t *testing.T) {
	deployConfig := resumeTestConfig()
	store := makeTestStore(t)
	seedSuccess(t, store, "app", "h1", "headsha")
	seedSuccess(t, store, "app", "h2", "headsha")

	dispatched := 0
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, resolver("headsha", nil),
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
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, noResolver,
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
	engine.ResumeIncompleteDeploys(context.Background(), deployConfig, store, noResolver,
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ }, slog.Default())

	if dispatched != 0 {
		t.Errorf("expected no dispatch for a repo with no known commit, got %d", dispatched)
	}
}

func TestResume_NilStore_Noop(t *testing.T) {
	dispatched := 0
	engine.ResumeIncompleteDeploys(context.Background(), resumeTestConfig(), nil, resolver("x", nil),
		func(_ context.Context, _ planner.RepoEvent) { dispatched++ }, slog.Default())
	if dispatched != 0 {
		t.Errorf("nil store should be a no-op, got %d dispatches", dispatched)
	}
}
