//go:build integration

package engine_test

// End-to-end integration test for the reload → teardown → redeploy cycle.
//
// It drives the real deploy and reconcile paths through:
//   - a real git mirror (clone + fetch + bundle extraction),
//   - a real ssh.Dialer over an in-process SSH server (exec + SFTP on localhost),
//   - the real compose.Executor against the local Docker Engine,
//   - the real runner, FileStore, and reconcile entry points.
//
// Requirements:
//   - git on PATH
//   - Docker + `docker compose` (v2) available
//
// Run with:
//
//	go test -tags integration -race -timeout 5m ./internal/engine/...

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"log/slog"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/engine"
	"github.com/jancernik/deeplo/internal/mirror"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/reporter"
	"github.com/jancernik/deeplo/internal/runner"
	"github.com/jancernik/deeplo/internal/ssh"
	"github.com/jancernik/deeplo/internal/state"
	"github.com/jancernik/deeplo/internal/testutils"
)

const integrationComposeYAML = `services:
  svc:
    image: busybox:latest
    command: ["sleep", "300"]
`

func requireGitAndCompose(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "compose", "version").Run(); err != nil {
		t.Skipf("docker compose not available: %v", err)
	}
}

// bareRepoWithCompose creates a bare git remote whose single commit contains a
// compose file at the repo root. Returns the clone URL (a local path) and HEAD.
func bareRepoWithCompose(t *testing.T) (bareURL, sha string) {
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
	if err := os.WriteFile(filepath.Join(work, "compose.yaml"), []byte(integrationComposeYAML), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(work, "add", ".")
	mustGit(work, "commit", "-m", "initial")
	mustGit(work, "remote", "add", "origin", bare)
	mustGit(work, "push", "origin", "main")

	out, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return bare, strings.TrimSpace(string(out))
}

// commitFile clones bareURL, adds/updates a file, commits, pushes to main, and
// returns the new HEAD sha. Used to produce a second commit so a redeploy runs.
func commitFile(t *testing.T, bareURL, name, content string) string {
	t.Helper()
	work := t.TempDir()
	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustGit("", "clone", bareURL, work)
	mustGit(work, "config", "user.email", "test@example.com")
	mustGit(work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(work, "add", ".")
	mustGit(work, "commit", "-m", "add "+name)
	mustGit(work, "push", "origin", "HEAD:main")

	out, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// waitDeploy polls until the latest deployment for (project, host) has finished
// at wantSha. Matching the sha is required because a redeploy leaves the prior
// terminal record in place, which would otherwise return immediately.
func waitDeploy(t *testing.T, store *state.FileStore, project, host, wantSha string, timeout time.Duration) *state.Deployment {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		latest, err := store.GetLatestDeployment(project, host)
		if err != nil {
			t.Fatalf("GetLatestDeployment: %v", err)
		}
		if latest != nil && latest.CommitSha == wantSha && latest.CompletedAt != nil {
			return latest
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s/%s deploy at %s to finish", project, host, wantSha)
	return nil
}

// integrationEnv bundles a fully wired deploy stack pointed at an in-process SSH
// server and a local bare git repo, for end-to-end tests.
type integrationEnv struct {
	store           *state.FileStore
	runner          *runner.Runner
	dialer          ssh.Dialer
	deployHandler   func(context.Context, planner.RepoEvent)
	bootstrapConfig *bootstrap.Config
	getConfig       func() *config.Config
	current         *atomic.Pointer[config.Config]
	configWith      *config.Config // config including project app → h1
	configWithout   *config.Config // config with the project removed
	mirrorHead      func(string, string) (string, bool)
	bareURL         string
	sha             string
	remoteDir       string
	serverPort      int
	keys            testutils.SSHKeys
}

func setupIntegration(t *testing.T) *integrationEnv {
	t.Helper()
	requireGitAndCompose(t)

	server, keys := testutils.StartSSHServer(t)
	bareURL, sha := bareRepoWithCompose(t)

	dataDir := t.TempDir()
	deployDir := t.TempDir() // acts as the remote host.deploy_dir

	bootstrapConfig := &bootstrap.Config{
		DataPath:          dataDir,
		SSHPrivateKeyFile: keys.PrivateKeyFile,
		SSHKnownHosts:     keys.KnownHostsFile,
		SSHHostKeyPolicy:  "accept-new",
		SSHUser:           "testuser",
	}
	sshEnv := mirror.SshEnv(keys.PrivateKeyFile, keys.KnownHostsFile, "accept-new")

	host := config.Host{Name: "h1", Address: "127.0.0.1", Port: server.Port(), User: "testuser", DeployDir: deployDir}
	repo := config.RepoConfig{Name: "app-repo", URL: bareURL, Branch: "main"}
	project := config.Project{
		Name:         "app",
		Repo:         "app-repo",
		ComposeFiles: []string{"compose.yaml"},
		Targets:      []string{"h1"},
		DeploySubdir: "app",
	}

	configWith := &config.Config{Hosts: []config.Host{host}, Repos: []config.RepoConfig{repo}, Projects: []config.Project{project}}
	configWithout := &config.Config{Hosts: []config.Host{host}, Repos: []config.RepoConfig{repo}}
	// Normalize the same way the loader does so defaults (e.g. persist_files:
	// [.env]) are applied, matching how the daemon runs in production.
	configWith.ApplyDefaults()
	configWithout.ApplyDefaults()

	current := &atomic.Pointer[config.Config]{}
	current.Store(configWith)
	getConfig := func() *config.Config { return current.Load() }

	store := state.NewFileStore(dataDir)
	if err := store.Init(); err != nil {
		t.Fatalf("store init: %v", err)
	}

	jobRunner := runner.New(runner.Config{MaxWorkers: 2, MaxHostConcurrency: 1}, slog.Default())
	jobRunner.Start()
	go engine.DrainResults(jobRunner.Results())
	t.Cleanup(jobRunner.Stop)

	dialer := ssh.NewDialer()
	deployHandler := engine.MakePushHandler(getConfig, bootstrapConfig, jobRunner, dialer, store, reporter.Noop(), sshEnv, slog.Default())

	env := &integrationEnv{
		store:           store,
		runner:          jobRunner,
		dialer:          dialer,
		deployHandler:   deployHandler,
		bootstrapConfig: bootstrapConfig,
		getConfig:       getConfig,
		current:         current,
		configWith:      configWith,
		configWithout:   configWithout,
		mirrorHead:      func(string, string) (string, bool) { return sha, true },
		bareURL:         bareURL,
		sha:             sha,
		remoteDir:       filepath.Join(deployDir, "app"),
		serverPort:      server.Port(),
		keys:            keys,
	}

	// Tear down the compose project so a failed assertion doesn't leak containers.
	t.Cleanup(func() { env.composeDown(t) })
	return env
}

// composeDown runs `docker compose -p app down` over the SSH server.
func (env *integrationEnv) composeDown(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := env.dialer.Dial(ctx, ssh.DialConfig{
		Address: "127.0.0.1", Port: env.serverPort, User: "testuser",
		PrivateKeyFile: env.keys.PrivateKeyFile, KnownHostsFile: env.keys.KnownHostsFile, HostKeyPolicy: "accept-new",
	})
	if err != nil {
		return
	}
	defer conn.Close()                                                     //nolint:errcheck
	_, _, _ = conn.Run(ctx, "docker compose -p app down --remove-orphans") //nolint:errcheck
}

func TestReloadTeardownRedeployCycle(t *testing.T) {
	env := setupIntegration(t)
	ctx := context.Background()

	// Phase 1: initial deploy
	env.deployHandler(ctx, planner.RepoEvent{
		Source: planner.TriggerWebhook, RepoName: "app-repo", Branch: "main", CommitSha: env.sha,
	})
	depl := waitDeploy(t, env.store, "app", "h1", env.sha, 3*time.Minute)
	if depl.Status != state.StatusSuccess {
		t.Fatalf("initial deploy status = %q, want success (err: %s)", depl.Status, depl.Error)
	}
	if _, err := os.Stat(filepath.Join(env.remoteDir, "compose.yaml")); err != nil {
		t.Fatalf("expected compose.yaml in remote dir after deploy: %v", err)
	}
	assertContainerRunning(t, "app", true)

	// Phase 2: reload removes the target → teardown
	env.current.Store(env.configWithout)
	engine.ReconcileRemovals(ctx, env.configWith, env.configWithout, env.bootstrapConfig, env.store, env.dialer, env.runner, env.getConfig, slog.Default())

	if _, err := os.Stat(env.remoteDir); !os.IsNotExist(err) {
		t.Errorf("expected remote dir removed after teardown, stat err = %v", err)
	}
	if latest, _ := env.store.GetLatestDeployment("app", "h1"); latest != nil {
		t.Errorf("expected state record deleted after genuine removal, got %s", latest.ID)
	}
	assertContainerRunning(t, "app", false)

	// Phase 3: reload re-adds the target → redeploy
	env.current.Store(env.configWith)
	engine.ReconcileAdditions(ctx, env.configWithout, env.configWith, env.store, env.mirrorHead, env.deployHandler, slog.Default())

	depl = waitDeploy(t, env.store, "app", "h1", env.sha, 3*time.Minute)
	if depl.Status != state.StatusSuccess {
		t.Fatalf("redeploy status = %q, want success (err: %s)", depl.Status, depl.Error)
	}
	if _, err := os.Stat(filepath.Join(env.remoteDir, "compose.yaml")); err != nil {
		t.Fatalf("expected compose.yaml in remote dir after redeploy: %v", err)
	}
	assertContainerRunning(t, "app", true)
}

// A deploy dropped by a prior shutdown (no container, remote dir, or state
// record) is brought back by startup resume, then idempotent on a second pass.
func TestResumeIncompleteDeployIntegration(t *testing.T) {
	env := setupIntegration(t)
	ctx := context.Background()

	// Deploy normally so the git mirror and repo state exist.
	env.deployHandler(ctx, planner.RepoEvent{
		Source: planner.TriggerWebhook, RepoName: "app-repo", Branch: "main", CommitSha: env.sha,
	})
	if depl := waitDeploy(t, env.store, "app", "h1", env.sha, 3*time.Minute); depl.Status != state.StatusSuccess {
		t.Fatalf("initial deploy status = %q, want success (err: %s)", depl.Status, depl.Error)
	}

	// Simulate a shutdown dropping the target mid-fan-out: container, remote dir, and record all gone.
	env.composeDown(t)
	if err := os.RemoveAll(env.remoteDir); err != nil {
		t.Fatalf("remove remote dir: %v", err)
	}
	if _, err := env.store.DeleteLatestDeployment("app", "h1"); err != nil {
		t.Fatalf("delete deployment record: %v", err)
	}
	assertContainerRunning(t, "app", false)

	engine.ResumeIncompleteDeploys(ctx, env.configWith, env.store, env.mirrorHead, nilFinder, env.deployHandler, slog.Default())
	resumed := waitDeploy(t, env.store, "app", "h1", env.sha, 3*time.Minute)
	if resumed.Status != state.StatusSuccess {
		t.Fatalf("resumed deploy status = %q, want success (err: %s)", resumed.Status, resumed.Error)
	}
	if _, err := os.Stat(filepath.Join(env.remoteDir, "compose.yaml")); err != nil {
		t.Fatalf("expected compose.yaml in remote dir after resume: %v", err)
	}
	assertContainerRunning(t, "app", true)

	// A second resume with everything up to date must not redeploy.
	engine.ResumeIncompleteDeploys(ctx, env.configWith, env.store, env.mirrorHead, nilFinder, env.deployHandler, slog.Default())
	time.Sleep(300 * time.Millisecond)
	after, err := env.store.GetLatestDeployment("app", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != resumed.ID {
		t.Errorf("second resume redeployed an up-to-date target (id %s → %s)", resumed.ID, after.ID)
	}
}

// assertContainerRunning checks whether the compose project has a running
// container, matching want.
func assertContainerRunning(t *testing.T, projectName string, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "compose", "-p", projectName, "ps", "--quiet").Output()
	if err != nil {
		t.Fatalf("docker compose ps: %v", err)
	}
	running := strings.TrimSpace(string(out)) != ""
	if running != want {
		t.Errorf("container running = %v, want %v (ps output: %q)", running, want, string(out))
	}
}

// By default a host-managed .env (not in the repo) survives a redeploy at a new
// commit, with no persist_files configuration.
func TestPersistFilesDefaultEnvSurvivesRedeploy(t *testing.T) {
	env := setupIntegration(t)
	ctx := context.Background()

	env.deployHandler(ctx, planner.RepoEvent{
		Source: planner.TriggerWebhook, RepoName: "app-repo", Branch: "main", CommitSha: env.sha,
	})
	if depl := waitDeploy(t, env.store, "app", "h1", env.sha, 3*time.Minute); depl.Status != state.StatusSuccess {
		t.Fatalf("initial deploy status = %q (err: %s)", depl.Status, depl.Error)
	}

	// Operator drops a host-managed secret into the live project dir.
	const secret = "SECRET=from-host\n"
	envPath := filepath.Join(env.remoteDir, ".env")
	if err := os.WriteFile(envPath, []byte(secret), 0600); err != nil {
		t.Fatalf("write host .env: %v", err)
	}

	// A new commit (no .env in the repo) triggers a redeploy that swaps the dir.
	sha2 := commitFile(t, env.bareURL, "bump.txt", "1")
	env.deployHandler(ctx, planner.RepoEvent{
		Source: planner.TriggerWebhook, RepoName: "app-repo", Branch: "main", CommitSha: sha2,
	})
	depl := waitDeploy(t, env.store, "app", "h1", sha2, 3*time.Minute)
	if depl.Status != state.StatusSuccess {
		t.Fatalf("redeploy status = %q (err: %s)", depl.Status, depl.Error)
	}

	// The host .env must have survived the atomic swap (default persist of .env).
	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read .env after redeploy: %v", err)
	}
	if string(got) != secret {
		t.Errorf("host .env was not persisted across redeploy: got %q, want %q", string(got), secret)
	}
}

// persist_files: [] disables persistence: a host .env is wiped by the redeploy's swap.
func TestPersistFilesOptOut(t *testing.T) {
	env := setupIntegration(t)
	ctx := context.Background()

	noPersist := *env.configWith
	projects := make([]config.Project, len(noPersist.Projects))
	copy(projects, noPersist.Projects)
	projects[0].PersistFiles = []string{}
	noPersist.Projects = projects
	env.current.Store(&noPersist)

	env.deployHandler(ctx, planner.RepoEvent{
		Source: planner.TriggerWebhook, RepoName: "app-repo", Branch: "main", CommitSha: env.sha,
	})
	if depl := waitDeploy(t, env.store, "app", "h1", env.sha, 3*time.Minute); depl.Status != state.StatusSuccess {
		t.Fatalf("initial deploy status = %q (err: %s)", depl.Status, depl.Error)
	}

	if err := os.WriteFile(filepath.Join(env.remoteDir, ".env"), []byte("SECRET=from-host\n"), 0600); err != nil {
		t.Fatalf("write host .env: %v", err)
	}

	sha2 := commitFile(t, env.bareURL, "bump.txt", "1")
	env.deployHandler(ctx, planner.RepoEvent{
		Source: planner.TriggerWebhook, RepoName: "app-repo", Branch: "main", CommitSha: sha2,
	})
	if depl := waitDeploy(t, env.store, "app", "h1", sha2, 3*time.Minute); depl.Status != state.StatusSuccess {
		t.Fatalf("redeploy status = %q (err: %s)", depl.Status, depl.Error)
	}

	if _, err := os.Stat(filepath.Join(env.remoteDir, ".env")); !os.IsNotExist(err) {
		t.Errorf("expected host .env to be wiped with persist_files: [], stat err = %v", err)
	}
}
