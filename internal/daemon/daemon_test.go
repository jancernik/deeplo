package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/engine"
	"github.com/jancernik/deeplo/internal/mirror"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/repowatcher"
	"github.com/jancernik/deeplo/internal/webhook"
)

func TestWebhookPushHandler_DynamicConfig(t *testing.T) {
	var configPtr atomic.Pointer[config.Config]
	configPtr.Store(&config.Config{}) // start with empty config - no repos
	getConfig := func() *config.Config { return configPtr.Load() }

	var dispatchCount int
	onDeploy := func(_ context.Context, _ planner.RepoEvent) { dispatchCount++ }

	h := engine.MakeWebhookPushHandler(getConfig, nil, onDeploy, nil, slog.Default())

	push := webhook.PushEvent{
		RepoFullName: "owner/myapp",
		Branch:       "main",
		CommitSha:    "aaa",
	}

	h(context.Background(), push)
	if dispatchCount != 0 {
		t.Fatalf("expected 0 dispatches before config update, got %d", dispatchCount)
	}

	configPtr.Store(&config.Config{
		Repos: []config.RepoConfig{
			{Name: "myapp", URL: "git@github.com:owner/myapp.git", Branch: "main", TriggerMode: config.TriggerModeWebhook},
		},
	})

	h(context.Background(), push)
	if dispatchCount != 1 {
		t.Errorf("expected 1 dispatch after config update, got %d", dispatchCount)
	}
}

// Regression: restart called from inside a watcher's own handler must not
// self-deadlock (old.Stop() waiting on the goroutine currently running).
func TestManagedWatcher_RestartFromInsideHandlerDoesNotDeadlock(t *testing.T) {
	restarted := make(chan struct{})

	// Build a first watcher whose sole handler triggers a restart of itself.
	var managed managedWatcher
	sub := repowatcher.Subscription{
		URL:      "fake://repo",
		Branch:   "main",
		Interval: 50 * time.Millisecond,
		Handler: func(ctx context.Context, _ string) {
			// This simulates onConfigReload being called from the poller handler.
			managed.restart(ctx, repowatcher.New(nil, nil, nil, slog.Default()))
			close(restarted)
		},
	}
	remoteSha := func(_ context.Context, _, _ string, _ []string) (string, error) {
		return "abc123", nil
	}
	managed.start(repowatcher.New([]repowatcher.Subscription{sub}, remoteSha, nil, slog.Default()), context.Background())

	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("restart deadlocked: watcher goroutine waited for itself")
	}
}

// isConfigRepo gates the reload hook: only a push to the config repo triggers a
// reload, and only when the source is git (configRepoFullName non-empty).
func TestIsConfigRepo(t *testing.T) {
	repos := []config.RepoConfig{
		{Name: "config", URL: "git@github.com:org/config.git"},
		{Name: "app", URL: "git@github.com:org/app.git"},
	}
	const configFullName = "org/config"

	cases := []struct {
		name               string
		repoName           string
		configRepoFullName string
		want               bool
	}{
		{"config repo matches", "config", configFullName, true},
		{"deploy repo does not match", "app", configFullName, false},
		{"unknown repo does not match", "other", configFullName, false},
		{"non-git source never matches", "config", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConfigRepo(tc.repoName, tc.configRepoFullName, repos); got != tc.want {
				t.Errorf("isConfigRepo(%q, %q) = %v, want %v", tc.repoName, tc.configRepoFullName, got, tc.want)
			}
		})
	}
}

// A repo whose name matches the config repo but whose URL points elsewhere is not
// treated as the config repo.
func TestIsConfigRepo_NameCollisionDifferentURL(t *testing.T) {
	repos := []config.RepoConfig{{Name: "config", URL: "git@github.com:someone-else/config.git"}}
	if isConfigRepo("config", "org/config", repos) {
		t.Error("URL owner mismatch must not count as the config repo")
	}
}

// resolveMirrorHead
//
// These need a real git binary (mirror.Open/LocalHead shell out). Deploy mirror
// lives at {dataPath}/repos/{slug}, config mirror at {dataPath}/config/repos/{slug}.

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// setupBareRepo creates a bare git remote and a working copy with one commit.
// Returns the bare repo path (usable as a clone source) and the HEAD SHA.
func setupBareRepo(t *testing.T) (bareDir, sha string) {
	t.Helper()

	base := t.TempDir()
	bare := filepath.Join(base, "remote.git")
	work := filepath.Join(base, "work")

	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	mustGit("", "init", "--bare", "--initial-branch=main", bare)
	mustGit("", "init", "--initial-branch=main", work)
	mustGit(work, "config", "user.email", "test@example.com")
	mustGit(work, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(work, "add", ".")
	mustGit(work, "commit", "-m", "initial")
	mustGit(work, "remote", "add", "origin", bare)
	mustGit(work, "push", "origin", "main")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = work
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return bare, strings.TrimSpace(string(out))
}

// cloneAsDeployMirror clones bareURL into the deploy mirror path ({dataPath}/repos/{slug}).
func cloneAsDeployMirror(t *testing.T, bareURL, dataPath string) {
	t.Helper()
	if _, err := mirror.Open(context.Background(), bareURL, dataPath, nil, slog.Default()); err != nil {
		t.Fatalf("clone deploy mirror: %v", err)
	}
}

// cloneAsConfigMirror clones bareURL into the config mirror path ({dataPath}/config/repos/{slug}).
func cloneAsConfigMirror(t *testing.T, bareURL, dataPath string) {
	t.Helper()
	configMirrorBase := filepath.Join(dataPath, "config")
	if _, err := mirror.Open(context.Background(), bareURL, configMirrorBase, nil, slog.Default()); err != nil {
		t.Fatalf("clone config mirror: %v", err)
	}
}

// resolveMirrorHead runs in ReconcileAdditions after the config mirror is fetched.
// The config mirror is checked first (SourceGit, guaranteed latest); the deploy
// mirror is the fallback for non-matching URLs and the sole source for SourceLocal.

func TestResolveMirrorHead_OnlyDeployMirror(t *testing.T) {
	requireGit(t)
	bare, sha := setupBareRepo(t)
	dataPath := t.TempDir()
	cloneAsDeployMirror(t, bare, dataPath)

	got, ok := resolveMirrorHead(bare, "main", dataPath, bootstrap.SourceGit, nil, slog.Default())
	if !ok {
		t.Fatal("expected ok=true when deploy mirror exists")
	}
	if got != sha {
		t.Errorf("sha = %q, want %q", got, sha)
	}
}

func TestResolveMirrorHead_OnlyConfigMirror(t *testing.T) {
	requireGit(t)
	bare, sha := setupBareRepo(t)
	dataPath := t.TempDir()
	cloneAsConfigMirror(t, bare, dataPath)

	got, ok := resolveMirrorHead(bare, "main", dataPath, bootstrap.SourceGit, nil, slog.Default())
	if !ok {
		t.Fatal("expected ok=true when config mirror exists")
	}
	if got != sha {
		t.Errorf("sha = %q, want %q", got, sha)
	}
}

func TestResolveMirrorHead_NeitherMirror(t *testing.T) {
	requireGit(t)
	bare, _ := setupBareRepo(t)
	dataPath := t.TempDir()

	_, ok := resolveMirrorHead(bare, "main", dataPath, bootstrap.SourceGit, nil, slog.Default())
	if ok {
		t.Error("expected ok=false when no mirrors exist")
	}
}

func TestResolveMirrorHead_SourceLocal_IgnoresConfigMirror(t *testing.T) {
	requireGit(t)
	bare, _ := setupBareRepo(t)
	dataPath := t.TempDir()
	cloneAsConfigMirror(t, bare, dataPath)

	_, ok := resolveMirrorHead(bare, "main", dataPath, bootstrap.SourceLocal, nil, slog.Default())
	if ok {
		t.Error("expected ok=false: SourceLocal must not consult the config mirror path")
	}
}

func TestResolveMirrorHead_BothMirrors_PrefersConfig(t *testing.T) {
	requireGit(t)
	bare, sha := setupBareRepo(t)
	dataPath := t.TempDir()
	cloneAsDeployMirror(t, bare, dataPath)
	cloneAsConfigMirror(t, bare, dataPath)

	got, ok := resolveMirrorHead(bare, "main", dataPath, bootstrap.SourceGit, nil, slog.Default())
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != sha {
		t.Errorf("sha = %q, want %q", got, sha)
	}
}

// Regression (commit-status visibility): on a config-push trigger the config
// mirror is at the new SHA while the deploy mirror is stale, so resolveMirrorHead
// must return the config mirror SHA to land on the just-pushed commit.
func TestResolveMirrorHead_ConfigMirrorAheadOfDeploy(t *testing.T) {
	requireGit(t)
	bare, _ := setupBareRepo(t)
	dataPath := t.TempDir()

	cloneAsDeployMirror(t, bare, dataPath) // deploy mirror fixed at sha1

	// Push a second commit and clone the config mirror from the updated bare repo.
	work := t.TempDir()
	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustGit("", "clone", bare, work)
	mustGit(work, "config", "user.email", "test@example.com")
	mustGit(work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "config.yml"), []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(work, "add", ".")
	mustGit(work, "commit", "-m", "config change")
	mustGit(work, "push", "origin", "main")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = work
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	sha2 := strings.TrimSpace(string(out))

	cloneAsConfigMirror(t, bare, dataPath) // config mirror at sha2

	got, ok := resolveMirrorHead(bare, "main", dataPath, bootstrap.SourceGit, nil, slog.Default())
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != sha2 {
		t.Errorf("sha = %q, want config mirror sha %q", got, sha2)
	}
}

func TestResolveMirrorHead_UnknownBranch(t *testing.T) {
	requireGit(t)
	bare, _ := setupBareRepo(t)
	dataPath := t.TempDir()
	cloneAsDeployMirror(t, bare, dataPath)

	_, ok := resolveMirrorHead(bare, "no-such-branch", dataPath, bootstrap.SourceGit, nil, slog.Default())
	if ok {
		t.Error("expected ok=false for a branch that does not exist in the mirror")
	}
}

// Multi-repo setup: deeplo config lives in configBare, app repos (appBare) are
// defined in it. Queries for appBare resolve from the deploy mirror, not the config mirror.

func TestResolveMirrorHead_SeparateRepos_DeployMirrorUsed(t *testing.T) {
	// Querying for appBare must return the deploy mirror SHA, not the config mirror's.
	requireGit(t)
	configBare, _ := setupBareRepo(t)
	appBare, appSHA := setupBareRepo(t)
	dataPath := t.TempDir()

	cloneAsConfigMirror(t, configBare, dataPath) // config mirror → config repo slug
	cloneAsDeployMirror(t, appBare, dataPath)    // deploy mirror → app repo slug

	got, ok := resolveMirrorHead(appBare, "main", dataPath, bootstrap.SourceGit, nil, slog.Default())
	if !ok {
		t.Fatal("expected ok=true when deploy mirror exists for the queried URL")
	}
	if got != appSHA {
		t.Errorf("sha = %q, want deploy mirror sha %q", got, appSHA)
	}
}

func TestResolveMirrorHead_SeparateRepos_NoDeployMirror_ReturnsNotFound(t *testing.T) {
	// With no deploy mirror for appBare, the config mirror must not be used for its URL.
	requireGit(t)
	configBare, _ := setupBareRepo(t)
	appBare, _ := setupBareRepo(t)
	dataPath := t.TempDir()

	cloneAsConfigMirror(t, configBare, dataPath) // config mirror exists for the config repo

	_, ok := resolveMirrorHead(appBare, "main", dataPath, bootstrap.SourceGit, nil, slog.Default())
	if ok {
		t.Error("expected ok=false: config mirror for a different URL must not match the queried repo")
	}
}
