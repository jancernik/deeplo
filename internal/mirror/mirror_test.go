package mirror_test

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jancernik/deeplo/internal/mirror"
)

// requireGit skips the test if git is not on PATH.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping")
	}
}

// setupRepo initialises a bare remote repo and a working repo with a single
// commit. Returns the bare repo path (usable as a clone URL) and the commit SHA.
func setupRepo(t *testing.T) (bareDir, workDir, sha string) {
	t.Helper()

	base := t.TempDir()
	bare := filepath.Join(base, "remote.git")
	work := filepath.Join(base, "work")

	mustRun := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	mustRun("", "init", "--bare", "--initial-branch=main", bare)
	mustRun("", "init", "--initial-branch=main", work)
	mustRun(work, "config", "user.email", "test@example.com")
	mustRun(work, "config", "user.name", "Test")

	appDir := filepath.Join(work, "apps", "myapp")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "compose.yaml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, ".env"), []byte("FOO=bar\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mustRun(work, "add", ".")
	mustRun(work, "commit", "-m", "initial commit")
	mustRun(work, "remote", "add", "origin", bare)
	mustRun(work, "push", "origin", "main")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = work
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return bare, work, string(out[:40])
}

// addCommit adds a new file to the working repo and pushes it to bare.
// Returns the new commit SHA.
func addCommit(t *testing.T, workDir, filename, content string) string {
	t.Helper()
	mustRun := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(workDir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun(workDir, "add", filename)
	mustRun(workDir, "commit", "-m", "add "+filename)
	mustRun(workDir, "push", "origin", "main")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return string(out[:40])
}

func TestNew_ClonesOnFirstCall(t *testing.T) {
	requireGit(t)
	bare, _, _ := setupRepo(t)
	dataPath := t.TempDir()

	repo, err := mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if repo == nil {
		t.Fatal("New returned nil repo")
	}
}

// Regression (clone race): git clone --mirror creates HEAD before all objects
// are fetched, so a concurrent Open used to see HEAD, skip the clone, and return
// a partial repo. The per-URL mutex in Open serialises callers so one clone runs.
func TestOpen_ConcurrentSameURL(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupRepo(t)
	dataPath := t.TempDir()

	const n = 5
	var wg sync.WaitGroup
	errs := make([]error, n)
	repos := make([]*mirror.Repo, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			repos[idx], errs[idx] = mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Open returned error: %v", i, err)
		}
	}
	for i, repo := range repos {
		if repo == nil {
			continue
		}
		if !repo.HasCommit(context.Background(), sha) {
			t.Errorf("goroutine %d: commit %s not found after concurrent Open", i, sha)
		}
	}
}

func TestNew_ReuseExistingClone(t *testing.T) {
	requireGit(t)
	bare, _, _ := setupRepo(t)
	dataPath := t.TempDir()

	ctx := context.Background()
	if _, err := mirror.Open(ctx, bare, dataPath, nil, slog.Default()); err != nil {
		t.Fatalf("first New: %v", err)
	}
	if _, err := mirror.Open(ctx, bare, dataPath, nil, slog.Default()); err != nil {
		t.Fatalf("second New: %v", err)
	}
}

func TestFind_ReturnsNilWhenNotCloned(t *testing.T) {
	requireGit(t)
	bare, _, _ := setupRepo(t)
	dataPath := t.TempDir()

	repo, err := mirror.Find(bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("Find: unexpected error: %v", err)
	}
	if repo != nil {
		t.Fatal("Find: expected nil before cloning, got non-nil")
	}
}

func TestFind_ReturnsRepoAfterClone(t *testing.T) {
	requireGit(t)
	bare, _, _ := setupRepo(t)
	dataPath := t.TempDir()

	ctx := context.Background()
	if _, err := mirror.Open(ctx, bare, dataPath, nil, slog.Default()); err != nil {
		t.Fatalf("New: %v", err)
	}
	repo, err := mirror.Find(bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if repo == nil {
		t.Fatal("Find: expected non-nil after cloning")
	}
}

func TestEnsureCommit_CommitAlreadyPresent(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupRepo(t)
	dataPath := t.TempDir()

	repo, err := mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := repo.EnsureCommit(context.Background(), sha); err != nil {
		t.Errorf("EnsureCommit: %v", err)
	}
}

func TestReadFile(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupRepo(t)
	dataPath := t.TempDir()

	repo, err := mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := []struct {
		path string
		want string
	}{
		{"apps/myapp/compose.yaml", "services: {}\n"},
		{"apps/myapp/.env", "FOO=bar\n"},
	}

	ctx := context.Background()
	for _, testCase := range cases {
		t.Run(testCase.path, func(t *testing.T) {
			got, err := repo.ReadFile(ctx, sha, testCase.path)
			if err != nil {
				t.Fatalf("ReadFile(%q): %v", testCase.path, err)
			}
			if string(got) != testCase.want {
				t.Errorf("content: got %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestReadFile_NonExistentPath(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupRepo(t)
	dataPath := t.TempDir()

	repo, _ := mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
	_, err := repo.ReadFile(context.Background(), sha, "no/such/file.yaml")
	if err == nil {
		t.Error("expected error for non-existent path, got nil")
	}
}

// ListFiles

func TestListFiles_SingleFile(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupRepo(t)
	dataPath := t.TempDir()

	repo, err := mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	files, err := repo.ListFiles(context.Background(), sha, "apps/myapp/compose.yaml")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "apps/myapp/compose.yaml" {
		t.Errorf("ListFiles = %v, want [apps/myapp/compose.yaml]", files)
	}
}

func TestListFiles_Directory_ExpandsRecursively(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupRepo(t)
	dataPath := t.TempDir()

	repo, err := mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	files, err := repo.ListFiles(context.Background(), sha, "apps/myapp")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("ListFiles = %v, want 2 entries", files)
	}
	found := make(map[string]bool)
	for _, f := range files {
		found[f] = true
	}
	for _, want := range []string{"apps/myapp/compose.yaml", "apps/myapp/.env"} {
		if !found[want] {
			t.Errorf("missing %q in ListFiles result %v", want, files)
		}
	}
}

func TestListFiles_NonExistentPath_ReturnsEmpty(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupRepo(t)
	dataPath := t.TempDir()

	repo, err := mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	files, err := repo.ListFiles(context.Background(), sha, "no/such/path")
	if err != nil {
		t.Fatalf("ListFiles: unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("ListFiles = %v, want empty for missing path", files)
	}
}

// SshEnv

func TestSshEnv_NoPrivateKey_ReturnsNil(t *testing.T) {
	if env := mirror.SshEnv("", "", ""); env != nil {
		t.Errorf("expected nil, got %v", env)
	}
}

func TestSshEnv_DefaultPolicy_AcceptNew(t *testing.T) {
	env := mirror.SshEnv("/key", "", "")
	if len(env) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(env))
	}
	if !contains(env[0], "accept-new") {
		t.Errorf("expected accept-new in %q", env[0])
	}
}

func TestSshEnv_StrictPolicy(t *testing.T) {
	env := mirror.SshEnv("/key", "", "strict")
	if !contains(env[0], "StrictHostKeyChecking=yes") {
		t.Errorf("expected StrictHostKeyChecking=yes in %q", env[0])
	}
}

func TestSshEnv_WithKnownHostsFile(t *testing.T) {
	env := mirror.SshEnv("/key", "/known_hosts", "")
	if !contains(env[0], "UserKnownHostsFile=/known_hosts") {
		t.Errorf("expected UserKnownHostsFile in %q", env[0])
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := range len(s) - len(substr) + 1 {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}

// RemoteSha

func TestRemoteSha_ExistingBranch(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupRepo(t)

	got, err := mirror.RemoteSha(context.Background(), bare, "main", nil)
	if err != nil {
		t.Fatalf("RemoteSha: %v", err)
	}
	if got != sha {
		t.Errorf("sha = %q, want %q", got, sha)
	}
}

func TestRemoteSha_NonExistentBranch_ReturnsEmpty(t *testing.T) {
	requireGit(t)
	bare, _, _ := setupRepo(t)

	got, err := mirror.RemoteSha(context.Background(), bare, "no-such-branch", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty SHA for missing branch, got %q", got)
	}
}

// DiffFiles

func TestDiffFiles(t *testing.T) {
	requireGit(t)
	bare, work, sha1 := setupRepo(t)
	dataPath := t.TempDir()

	repo, err := mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	sha2 := addCommit(t, work, "newfile.txt", "hello\n")
	if err := repo.EnsureCommit(context.Background(), sha2); err != nil {
		t.Fatalf("EnsureCommit: %v", err)
	}

	files, err := repo.DiffFiles(context.Background(), sha1, sha2)
	if err != nil {
		t.Fatalf("DiffFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "newfile.txt" {
		t.Errorf("DiffFiles = %v, want [newfile.txt]", files)
	}
}

// Regression (mass-redeploy incident): a commit that exactly reverts the previous
// one leaves the two trees identical, so the diff has no files. The result must be
// an empty non-nil slice — a nil slice reads as "unknown diff" to the planner,
// which deploys every target.
func TestDiffFiles_IdenticalTrees_EmptyNonNil(t *testing.T) {
	requireGit(t)
	bare, work, baseSha := setupRepo(t)
	dataPath := t.TempDir()

	addCommit(t, work, "apps/myapp/.env", "FOO=broken\n")           // bad commit
	revertSha := addCommit(t, work, "apps/myapp/.env", "FOO=bar\n") // exact revert of it

	repo, err := mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	files, err := repo.DiffFiles(context.Background(), baseSha, revertSha)
	if err != nil {
		t.Fatalf("DiffFiles: %v", err)
	}
	if files == nil {
		t.Fatal("DiffFiles returned nil for an empty diff, want empty non-nil slice")
	}
	if len(files) != 0 {
		t.Errorf("DiffFiles = %v, want empty (trees are identical)", files)
	}
}

// EnsureCommit fetch path

func TestEnsureCommit_FetchesWhenMissing(t *testing.T) {
	requireGit(t)
	bare, work, _ := setupRepo(t)
	dataPath := t.TempDir()

	// Clone at sha1, then add a commit to the remote.
	repo, err := mirror.Open(context.Background(), bare, dataPath, nil, slog.Default())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	sha2 := addCommit(t, work, "after-clone.txt", "new\n")

	// sha2 is not in the local mirror yet - EnsureCommit must fetch it.
	if err := repo.EnsureCommit(context.Background(), sha2); err != nil {
		t.Fatalf("EnsureCommit after new remote commit: %v", err)
	}
	if !repo.HasCommit(context.Background(), sha2) {
		t.Error("commit still missing after EnsureCommit")
	}
}
