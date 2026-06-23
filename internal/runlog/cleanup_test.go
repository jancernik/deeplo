package runlog

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeFileWithAge(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte("log"), 0644); err != nil {
		t.Fatal(err)
	}
	modTime := time.Now().Add(-age)
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// TestCleanDir_DeletesOnlyStaleFiles verifies files older than the cutoff are
// removed while fresh files and subdirectories are left untouched.
func TestCleanDir_DeletesOnlyStaleFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.log")
	fresh := filepath.Join(dir, "fresh.log")
	writeFileWithAge(t, old, 10*24*time.Hour)
	writeFileWithAge(t, fresh, 1*time.Hour)

	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	cleanDir(dir, cutoff, 7, quietLogger())

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old file should have been deleted, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh file should remain, got err = %v", err)
	}
	if _, err := os.Stat(subdir); err != nil {
		t.Errorf("subdirectory should remain, got err = %v", err)
	}
}

// TestCleanDir_MissingDirIsNoError verifies a nonexistent directory is silently
// ignored (it is not an error worth surfacing).
func TestCleanDir_MissingDirIsNoError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	// Must not panic; nothing to assert beyond it returning cleanly.
	cleanDir(missing, time.Now(), 7, quietLogger())
}

// TestStartCleanup_RunsOnceAtStartup verifies StartCleanup performs an immediate
// sweep (before its 24h ticker would ever fire) and deletes stale files.
func TestStartCleanup_RunsOnceAtStartup(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale.log")
	writeFileWithAge(t, stale, 30*24*time.Hour)

	StartCleanup(t.Context(), 7, quietLogger(), dir)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stale); os.IsNotExist(err) {
			return // deleted as expected
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stale file was not deleted by the startup sweep")
}

// TestStartCleanup_DisabledByZeroRetention verifies retention <= 0 disables
// cleanup entirely: a stale file survives.
func TestStartCleanup_DisabledByZeroRetention(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale.log")
	writeFileWithAge(t, stale, 30*24*time.Hour)

	StartCleanup(t.Context(), 0, quietLogger(), dir)

	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("file should survive when retention is disabled, got err = %v", err)
	}
}
