// Package mirror manages bare git mirrors on disk.
package mirror

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/jancernik/deeplo/internal/utils"
)

type Repo struct {
	dir    string
	url    string
	sshEnv []string
	logger *slog.Logger
}

var reSlug = regexp.MustCompile(`[^a-zA-Z0-9.\-]`)
var cloneMu sync.Map

func slugFromURL(url string) string {
	slug := url

	if i := strings.Index(slug, "://"); i >= 0 {
		slug = slug[i+3:]
	}

	if i := strings.Index(slug, "@"); i >= 0 {
		slug = slug[i+1:]
	}

	if colon := strings.Index(slug, ":"); colon >= 0 {
		if slash := strings.Index(slug, "/"); slash < 0 || colon < slash {
			slug = slug[:colon] + "/" + slug[colon+1:]
		}
	}
	slug = strings.TrimSuffix(slug, ".git")
	return reSlug.ReplaceAllString(slug, "_")
}

func run(ctx context.Context, dir string, env []string, name string, args ...string) error {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w\n%s", err, out.String())
	}
	return nil
}

// Builds the GIT_SSH_COMMAND environment slice for git operations.
func SshEnv(privateKeyFile, knownHostsFile, hostKeyPolicy string) []string {
	if privateKeyFile == "" {
		return nil
	}
	checking := "accept-new"
	if hostKeyPolicy == "strict" {
		checking = "yes"
	}
	cmd := "ssh -i " + privateKeyFile + " -o StrictHostKeyChecking=" + checking
	if knownHostsFile != "" {
		cmd += " -o UserKnownHostsFile=" + knownHostsFile
	}
	return []string{"GIT_SSH_COMMAND=" + cmd}
}

// Returns the current HEAD sha for a branch via git ls-remote.
// Returns an empty string when the branch does not exist on the remote.
func RemoteSha(ctx context.Context, url, branch string, sshEnv []string) (string, error) {
	ref := "refs/heads/" + branch
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "ls-remote", url, ref)
	cmd.Env = append(os.Environ(), sshEnv...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git ls-remote %s %s: %w\n%s", url, ref, err, stderr.String())
	}
	line := strings.TrimSpace(stdout.String())
	if line == "" {
		return "", nil
	}
	parts := strings.Fields(line)
	if len(parts) < 1 || len(parts[0]) < 40 {
		return "", fmt.Errorf("unexpected git ls-remote output: %q", line)
	}
	return parts[0], nil
}

// Returns a repo for url, cloning it as a bare mirror if it does not exist yet.
func Open(ctx context.Context, url, dataPath string, sshEnv []string, logger *slog.Logger) (*Repo, error) {
	logger = logger.With("component", "mirror")
	dir := filepath.Join(dataPath, "repos", slugFromURL(url))

	mu, _ := cloneMu.LoadOrStore(dir, new(sync.Mutex))
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	if _, err := os.Stat(filepath.Join(dir, "HEAD")); os.IsNotExist(err) {
		logger.Info("cloning", "url", url, "dir", dir)
		if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
			return nil, fmt.Errorf("create repo dir: %w", err)
		}
		if err := run(ctx, "", sshEnv, "git", "clone", "--mirror", url, dir); err != nil {
			return nil, fmt.Errorf("git clone %s: %w", url, err)
		}
	}

	return &Repo{dir: dir, url: url, sshEnv: sshEnv, logger: logger}, nil
}

// Returns the extra environment for a git command scoped to this mirror, to be
// appended to os.Environ(). GIT_DIR names the bare repo explicitly so commands
// work even under git's safe.bareRepository=explicit hardening, which otherwise
// refuses auto-discovered bare repositories.
func (repo *Repo) gitEnv() []string {
	return append([]string{"GIT_DIR=" + repo.dir}, repo.sshEnv...)
}

// Returns an existing local mirror for url, or nil if it has not been cloned yet.
func Find(url, dataPath string, sshEnv []string, logger *slog.Logger) (*Repo, error) {
	logger = logger.With("component", "mirror")
	dir := filepath.Join(dataPath, "repos", slugFromURL(url))
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat repo dir: %w", err)
	}
	return &Repo{dir: dir, url: url, sshEnv: sshEnv, logger: logger}, nil
}

// Returns the sha that the given branch points to in the local mirror.
func (repo *Repo) LocalHead(branch string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "refs/heads/"+branch)
	cmd.Dir = repo.dir
	cmd.Env = append(os.Environ(), repo.gitEnv()...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse refs/heads/%s: %w", branch, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Reports whether sha exists in the local mirror.
func (repo *Repo) HasCommit(ctx context.Context, sha string) bool {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "-t", sha)
	cmd.Dir = repo.dir
	cmd.Env = append(os.Environ(), repo.gitEnv()...)
	return cmd.Run() == nil
}

// Guarantees that the sha is present in the local mirror.
func (repo *Repo) EnsureCommit(ctx context.Context, sha string) error {
	if repo.HasCommit(ctx, sha) {
		return nil
	}

	repo.logger.Info("fetching", "url", repo.url, "sha", utils.ShortSha(sha))
	err := run(ctx, repo.dir, repo.gitEnv(), "git", "fetch", "--prune", "--force", "origin")

	if repo.HasCommit(ctx, sha) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("git fetch %s: %w", repo.url, err)
	}
	return fmt.Errorf("commit %s not found in %s after fetch", sha, repo.url)
}

// Returns the paths of all files under path at the given commit.
func (repo *Repo) ListFiles(ctx context.Context, sha, path string) ([]string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "ls-tree", "-r", "--name-only", sha, "--", path)
	cmd.Dir = repo.dir
	cmd.Env = append(os.Environ(), repo.gitEnv()...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-tree %s %s: %w\n%s", utils.ShortSha(sha), path, err, stderr.String())
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// Returns the contents of filePath at the given commit.
func (repo *Repo) ReadFile(ctx context.Context, sha, filePath string) ([]byte, error) {
	ref := sha + ":" + filePath
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "show", ref)
	cmd.Dir = repo.dir
	cmd.Env = append(os.Environ(), repo.gitEnv()...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git show %s: %w\n%s", ref, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// IsAncestor reports whether ancestor is reachable from descendant in the local mirror.
func (repo *Repo) IsAncestor(ctx context.Context, ancestor, descendant string) bool {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = repo.dir
	cmd.Env = append(os.Environ(), repo.gitEnv()...)
	return cmd.Run() == nil
}

// Returns the list of files changed between two commits.
func (repo *Repo) DiffFiles(ctx context.Context, oldSha, newSha string) ([]string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", oldSha, newSha)
	cmd.Dir = repo.dir
	cmd.Env = append(os.Environ(), repo.gitEnv()...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff --name-only %s %s: %w\n%s",
			oldSha[:min(8, len(oldSha))], newSha[:min(8, len(newSha))], err, stderr.String())
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}
