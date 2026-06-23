package engine

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jancernik/deeplo/internal/compose"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/mirror"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping")
	}
}

// setupBundleRepo creates a bare remote with a standard set of files under
// apps/myapp/ and returns (bareDir, workDir, sha).
//
// Files created:
//
//	apps/myapp/compose.yaml
//	apps/myapp/config/app.conf
func setupBundleRepo(t *testing.T) (bareDir, workDir, sha string) {
	t.Helper()

	base := t.TempDir()
	bare := filepath.Join(base, "remote.git")
	work := filepath.Join(base, "work")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writeFile := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	run("", "init", "--bare", "--initial-branch=main", bare)
	run("", "init", "--initial-branch=main", work)
	run(work, "config", "user.email", "test@example.com")
	run(work, "config", "user.name", "Test")

	appDir := filepath.Join(work, "apps", "myapp")
	writeFile(filepath.Join(appDir, "compose.yaml"), "services: {}\n")
	writeFile(filepath.Join(appDir, "config", "app.conf"), "key=value\n")

	run(work, "add", ".")
	run(work, "commit", "-m", "initial")
	run(work, "remote", "add", "origin", bare)
	run(work, "push", "origin", "main")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = work
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return bare, work, string(out[:40])
}

// openMirror clones bare into a temp data dir and returns the mirror.
func openMirror(t *testing.T, bare string) *mirror.Repo {
	t.Helper()
	ctx := context.Background()
	repo, err := mirror.Open(ctx, bare, t.TempDir(), nil, slog.Default())
	if err != nil {
		t.Fatalf("mirror.Open: %v", err)
	}
	return repo
}

// bundleNames returns the RemoteName of every file in the bundle.
func bundleNames(bundle *compose.Bundle) map[string]bool {
	names := make(map[string]bool, len(bundle.Files))
	for _, f := range bundle.Files {
		names[f.RemoteName] = true
	}
	return names
}

func TestBuildBundle_ExtraFiles_Included(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupBundleRepo(t)
	repo := openMirror(t, bare)

	proj := config.Project{
		RepoSubdir:   "apps/myapp",
		ComposeFiles: []string{"compose.yaml"},
		ExtraFiles:   []string{"config/app.conf"},
	}
	bundle, err := buildBundle(context.Background(), repo, sha, proj, t.TempDir())
	if err != nil {
		t.Fatalf("buildBundle: %v", err)
	}

	if len(bundle.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(bundle.Files))
	}
	names := bundleNames(bundle)
	if !names["config/app.conf"] {
		t.Error("missing config/app.conf")
	}
}

func TestBuildBundle_ExtraFiles_PathPreserved(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupBundleRepo(t)
	repo := openMirror(t, bare)

	proj := config.Project{
		RepoSubdir:   "apps/myapp",
		ComposeFiles: []string{"compose.yaml"},
		ExtraFiles:   []string{"config/app.conf"},
	}
	tmpDir := t.TempDir()
	bundle, err := buildBundle(context.Background(), repo, sha, proj, tmpDir)
	if err != nil {
		t.Fatalf("buildBundle: %v", err)
	}

	var extraFile *compose.BundleFile
	for i := range bundle.Files {
		if bundle.Files[i].RemoteName == "config/app.conf" {
			extraFile = &bundle.Files[i]
			break
		}
	}
	if extraFile == nil {
		t.Fatal("config/app.conf not in bundle")
	}
	// LocalPath must mirror the subdirectory structure so it lands at config/app.conf on the remote.
	if _, err := os.Stat(extraFile.LocalPath); err != nil {
		t.Errorf("local file for config/app.conf not on disk: %v", err)
	}
	content, err := os.ReadFile(extraFile.LocalPath)
	if err != nil {
		t.Fatalf("read local file: %v", err)
	}
	if string(content) != "key=value\n" {
		t.Errorf("content = %q, want %q", content, "key=value\n")
	}
}

func TestBuildBundle_ComposeAndExtraFiles_Together(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupBundleRepo(t)
	repo := openMirror(t, bare)

	proj := config.Project{
		RepoSubdir:   "apps/myapp",
		ComposeFiles: []string{"compose.yaml"},
		ExtraFiles:   []string{"config/app.conf"},
	}
	bundle, err := buildBundle(context.Background(), repo, sha, proj, t.TempDir())
	if err != nil {
		t.Fatalf("buildBundle: %v", err)
	}

	if len(bundle.Files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(bundle.Files), bundle.Files)
	}
	names := bundleNames(bundle)
	for _, want := range []string{"compose.yaml", "config/app.conf"} {
		if !names[want] {
			t.Errorf("missing %s", want)
		}
	}
}

func TestBuildBundle_OrderPreserved(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupBundleRepo(t)
	repo := openMirror(t, bare)

	proj := config.Project{
		RepoSubdir:   "apps/myapp",
		ComposeFiles: []string{"compose.yaml"},
		ExtraFiles:   []string{"config/app.conf"},
	}
	bundle, err := buildBundle(context.Background(), repo, sha, proj, t.TempDir())
	if err != nil {
		t.Fatalf("buildBundle: %v", err)
	}

	want := []string{"compose.yaml", "config/app.conf"}
	if len(bundle.Files) != len(want) {
		t.Fatalf("expected %d files, got %d", len(want), len(bundle.Files))
	}
	for i, f := range bundle.Files {
		if f.RemoteName != want[i] {
			t.Errorf("files[%d].RemoteName = %q, want %q", i, f.RemoteName, want[i])
		}
	}
}

func TestBuildBundle_NoExtraFiles_OnlyComposeFile(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupBundleRepo(t)
	repo := openMirror(t, bare)

	proj := config.Project{
		RepoSubdir:   "apps/myapp",
		ComposeFiles: []string{"compose.yaml"},
	}
	bundle, err := buildBundle(context.Background(), repo, sha, proj, t.TempDir())
	if err != nil {
		t.Fatalf("buildBundle: %v", err)
	}
	if len(bundle.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(bundle.Files))
	}
}

func TestBuildBundle_ExtraFiles_DirectoryExpanded(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupBundleRepo(t)
	repo := openMirror(t, bare)

	// "config" is a directory in the repo; buildBundle should expand it recursively.
	proj := config.Project{
		RepoSubdir:   "apps/myapp",
		ComposeFiles: []string{"compose.yaml"},
		ExtraFiles:   []string{"config"},
	}
	bundle, err := buildBundle(context.Background(), repo, sha, proj, t.TempDir())
	if err != nil {
		t.Fatalf("buildBundle: %v", err)
	}

	names := bundleNames(bundle)
	if !names["compose.yaml"] {
		t.Error("missing compose.yaml")
	}
	if !names["config/app.conf"] {
		t.Errorf("config dir not expanded: got %v", names)
	}
}

func TestBuildBundle_MissingComposeFile(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupBundleRepo(t)
	repo := openMirror(t, bare)

	proj := config.Project{
		RepoSubdir:   "apps/myapp",
		ComposeFiles: []string{"nonexistent.yaml"},
	}
	_, err := buildBundle(context.Background(), repo, sha, proj, t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing compose file, got nil")
	}
}

func TestBuildBundle_MissingExtraFile(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupBundleRepo(t)
	repo := openMirror(t, bare)

	proj := config.Project{
		RepoSubdir:   "apps/myapp",
		ComposeFiles: []string{"compose.yaml"},
		ExtraFiles:   []string{"config/nonexistent.conf"},
	}
	_, err := buildBundle(context.Background(), repo, sha, proj, t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing extra file, got nil")
	}
}

func TestBuildBundle_EmptyExtraFiles_IsNoop(t *testing.T) {
	requireGit(t)
	bare, _, sha := setupBundleRepo(t)
	repo := openMirror(t, bare)

	proj := config.Project{
		RepoSubdir:   "apps/myapp",
		ComposeFiles: []string{"compose.yaml"},
		ExtraFiles:   []string{},
	}
	bundle, err := buildBundle(context.Background(), repo, sha, proj, t.TempDir())
	if err != nil {
		t.Fatalf("buildBundle: %v", err)
	}
	if len(bundle.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(bundle.Files))
	}
}
