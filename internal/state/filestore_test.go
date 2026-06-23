package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *FileStore {
	t.Helper()
	store := NewFileStore(t.TempDir())
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return store
}

func newTestDeployment(projectName, hostName string) *Deployment {
	return &Deployment{
		ID:        NewID(),
		Project:   projectName,
		Host:      hostName,
		CommitSha: "abc1234",
		Branch:    "main",
		Status:    StatusSuccess,
		StartedAt: time.Now().UTC(),
	}
}

// Init

func TestInit(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, subdirectory := range []string{
		"state/poll",
		"history/runs",
	} {
		path := filepath.Join(dir, subdirectory)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected directory %q to exist after Init", path)
		}
	}
}

func TestInitIdempotent(t *testing.T) {
	store := newTestStore(t)
	if err := store.Init(); err != nil {
		t.Fatalf("second Init call: %v", err)
	}
}

// SaveDeployment

// Deployment IDs containing path separators must not write outside runs/.
func TestSaveDeploymentRejectsPathTraversalID(t *testing.T) {
	store := newTestStore(t)

	traversalIDs := []string{
		"../../malicious",
		"../escape",
		"sub/dir",
		"back\\slash",
	}
	for _, id := range traversalIDs {
		deployment := newTestDeployment("app", "host-1")
		deployment.ID = id
		if err := store.SaveDeployment(deployment); err == nil {
			t.Errorf("SaveDeployment with ID %q: expected error, got nil", id)
		}
	}
}

func TestSaveDeploymentUpdatesStatus(t *testing.T) {
	store := newTestStore(t)
	deployment := newTestDeployment("app", "host-1")
	deployment.Status = StatusPending

	if err := store.SaveDeployment(deployment); err != nil {
		t.Fatalf("save pending: %v", err)
	}

	deployment.Status = StatusRunning
	if err := store.SaveDeployment(deployment); err != nil {
		t.Fatalf("save running: %v", err)
	}

	completedAt := time.Now().UTC()
	deployment.Status = StatusSuccess
	deployment.CompletedAt = &completedAt
	if err := store.SaveDeployment(deployment); err != nil {
		t.Fatalf("save success: %v", err)
	}

	retrieved, err := store.GetLatestDeployment(deployment.Project, deployment.Host)
	if err != nil {
		t.Fatalf("GetLatestDeployment: %v", err)
	}
	if retrieved.Status != StatusSuccess {
		t.Errorf("Status: got %q, want success", retrieved.Status)
	}
	if retrieved.CompletedAt == nil {
		t.Error("CompletedAt should be set")
	}
}

// A save with an earlier StartedAt must not overwrite the latest index entry.
func TestSaveDeploymentOlderDoesNotReplaceLatest(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()

	laterDeployment := newTestDeployment("app", "host-1")
	laterDeployment.StartedAt = now
	laterDeployment.CommitSha = "newer"

	earlierDeployment := newTestDeployment("app", "host-1")
	earlierDeployment.StartedAt = now.Add(-time.Minute)
	earlierDeployment.CommitSha = "older"

	if err := store.SaveDeployment(laterDeployment); err != nil {
		t.Fatalf("save later deployment: %v", err)
	}
	if err := store.SaveDeployment(earlierDeployment); err != nil {
		t.Fatalf("save earlier deployment: %v", err)
	}

	latest, err := store.GetLatestDeployment("app", "host-1")
	if err != nil {
		t.Fatalf("GetLatestDeployment: %v", err)
	}
	if latest.CommitSha != "newer" {
		t.Errorf("expected newer to remain latest, got CommitSha=%q", latest.CommitSha)
	}
}

// On equal StartedAt the most recent save wins; strict .After would drop it.
func TestSaveDeploymentSameTimestampLastWriteWins(t *testing.T) {
	store := newTestStore(t)
	sharedTime := time.Now().UTC()

	firstDeployment := newTestDeployment("app", "host-1")
	firstDeployment.StartedAt = sharedTime
	firstDeployment.CommitSha = "first"

	secondDeployment := newTestDeployment("app", "host-1")
	secondDeployment.StartedAt = sharedTime
	secondDeployment.CommitSha = "second"

	if err := store.SaveDeployment(firstDeployment); err != nil {
		t.Fatalf("save first: %v", err)
	}
	if err := store.SaveDeployment(secondDeployment); err != nil {
		t.Fatalf("save second: %v", err)
	}

	latest, err := store.GetLatestDeployment("app", "host-1")
	if err != nil {
		t.Fatalf("GetLatestDeployment: %v", err)
	}
	if latest.CommitSha != "second" {
		t.Errorf("expected second (last write) to win on equal timestamp, got CommitSha=%q", latest.CommitSha)
	}
}

// GetLatestDeployment

func TestGetLatestDeployment(t *testing.T) {
	store := newTestStore(t)

	earlierDeployment := newTestDeployment("paperless", "vm-docker-1")
	earlierDeployment.CommitSha = "aaa"
	if err := store.SaveDeployment(earlierDeployment); err != nil {
		t.Fatalf("save earlier: %v", err)
	}

	laterDeployment := newTestDeployment("paperless", "vm-docker-1")
	laterDeployment.CommitSha = "bbb"
	laterDeployment.StartedAt = earlierDeployment.StartedAt.Add(time.Second)
	if err := store.SaveDeployment(laterDeployment); err != nil {
		t.Fatalf("save later: %v", err)
	}

	retrieved, err := store.GetLatestDeployment("paperless", "vm-docker-1")
	if err != nil {
		t.Fatalf("GetLatestDeployment: %v", err)
	}
	if retrieved == nil {
		t.Fatal("GetLatestDeployment returned nil")
	}
	if retrieved.CommitSha != "bbb" {
		t.Errorf("CommitSha: got %q, want bbb (latest)", retrieved.CommitSha)
	}
}

func TestGetLatestDeploymentNotFound(t *testing.T) {
	store := newTestStore(t)
	retrieved, err := store.GetLatestDeployment("nonexistent", "host")
	if err != nil {
		t.Fatalf("GetLatestDeployment on empty store: %v", err)
	}
	if retrieved != nil {
		t.Errorf("expected nil for unknown project/host, got %v", retrieved)
	}
}

// Saves for one (project, host) pair must not affect another pair's entry.
func TestGetLatestDeploymentIsolation(t *testing.T) {
	store := newTestStore(t)

	appAHost1 := newTestDeployment("app-a", "host-1")
	appAHost1.CommitSha = "aaaa"
	if err := store.SaveDeployment(appAHost1); err != nil {
		t.Fatalf("save app-a/host-1: %v", err)
	}

	appBHost1 := newTestDeployment("app-b", "host-1")
	appBHost1.CommitSha = "bbbb"
	if err := store.SaveDeployment(appBHost1); err != nil {
		t.Fatalf("save app-b/host-1: %v", err)
	}

	retrieved, err := store.GetLatestDeployment("app-a", "host-1")
	if err != nil {
		t.Fatalf("GetLatestDeployment: %v", err)
	}
	if retrieved == nil || retrieved.CommitSha != "aaaa" {
		t.Errorf("app-a/host-1 should not be affected by app-b/host-1 save: got %v", retrieved)
	}
}

// DeleteLatestDeployment

func TestDeleteLatestDeployment(t *testing.T) {
	store := newTestStore(t)

	deployment := newTestDeployment("paperless", "vm-docker-1")
	if err := store.SaveDeployment(deployment); err != nil {
		t.Fatalf("save: %v", err)
	}

	removed, err := store.DeleteLatestDeployment("paperless", "vm-docker-1")
	if err != nil {
		t.Fatalf("DeleteLatestDeployment: %v", err)
	}
	if !removed {
		t.Error("expected removed=true for existing entry")
	}

	latest, err := store.GetLatestDeployment("paperless", "vm-docker-1")
	if err != nil {
		t.Fatalf("GetLatestDeployment: %v", err)
	}
	if latest != nil {
		t.Errorf("expected nil after delete, got %v", latest)
	}

	history, err := store.ListDeployments("paperless", "vm-docker-1", 10)
	if err != nil {
		t.Fatalf("ListDeployments after delete: %v", err)
	}
	if len(history) != 1 || history[0].ID != deployment.ID {
		t.Errorf("run history should survive index deletion, got %v", history)
	}
}

func TestDeleteLatestDeploymentMissingEntry(t *testing.T) {
	store := newTestStore(t)

	other := newTestDeployment("app-a", "host-1")
	if err := store.SaveDeployment(other); err != nil {
		t.Fatalf("save: %v", err)
	}

	removed, err := store.DeleteLatestDeployment("app-b", "host-1")
	if err != nil {
		t.Fatalf("DeleteLatestDeployment: %v", err)
	}
	if removed {
		t.Error("expected removed=false for missing entry")
	}

	latest, err := store.GetLatestDeployment("app-a", "host-1")
	if err != nil {
		t.Fatalf("GetLatestDeployment: %v", err)
	}
	if latest == nil {
		t.Error("unrelated entry should not be affected")
	}
}

func TestDeleteLatestDeploymentEmptyStore(t *testing.T) {
	store := newTestStore(t)

	removed, err := store.DeleteLatestDeployment("paperless", "vm-docker-1")
	if err != nil {
		t.Fatalf("DeleteLatestDeployment on empty store: %v", err)
	}
	if removed {
		t.Error("expected removed=false on empty store")
	}
}

// ListDeployments

func TestListDeployments(t *testing.T) {
	store := newTestStore(t)

	for i := range 5 {
		deployment := newTestDeployment("app", "host-1")
		deployment.StartedAt = time.Now().UTC().Add(time.Duration(i) * time.Second)
		deployment.ID = NewID()
		if err := store.SaveDeployment(deployment); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	otherDeployment := newTestDeployment("other", "host-1")
	if err := store.SaveDeployment(otherDeployment); err != nil {
		t.Fatalf("save other: %v", err)
	}

	t.Run("filter by project", func(t *testing.T) {
		deployments, err := store.ListDeployments("app", "", 0)
		if err != nil {
			t.Fatalf("ListDeployments: %v", err)
		}
		if len(deployments) != 5 {
			t.Errorf("count: got %d, want 5", len(deployments))
		}
	})

	t.Run("filter by host", func(t *testing.T) {
		deployments, err := store.ListDeployments("", "host-1", 0)
		if err != nil {
			t.Fatalf("ListDeployments: %v", err)
		}
		if len(deployments) != 6 {
			t.Errorf("count: got %d, want 6", len(deployments))
		}
	})

	t.Run("filter by project and host", func(t *testing.T) {
		deployments, err := store.ListDeployments("app", "host-1", 0)
		if err != nil {
			t.Fatalf("ListDeployments: %v", err)
		}
		if len(deployments) != 5 {
			t.Errorf("count: got %d, want 5", len(deployments))
		}
	})

	t.Run("limit applied", func(t *testing.T) {
		deployments, err := store.ListDeployments("app", "", 3)
		if err != nil {
			t.Fatalf("ListDeployments: %v", err)
		}
		if len(deployments) != 3 {
			t.Errorf("count with limit: got %d, want 3", len(deployments))
		}
	})

	t.Run("no filter returns all", func(t *testing.T) {
		deployments, err := store.ListDeployments("", "", 0)
		if err != nil {
			t.Fatalf("ListDeployments: %v", err)
		}
		if len(deployments) != 6 {
			t.Errorf("unfiltered count: got %d, want 6", len(deployments))
		}
	})

	t.Run("results sorted newest-first", func(t *testing.T) {
		deployments, err := store.ListDeployments("app", "", 0)
		if err != nil {
			t.Fatalf("ListDeployments: %v", err)
		}
		for i := 1; i < len(deployments); i++ {
			if deployments[i].StartedAt.After(deployments[i-1].StartedAt) {
				t.Errorf("list not sorted newest-first at index %d", i)
			}
		}
	})
}

// A corrupted run file yields a non-nil error alongside the valid deployments.
func TestListDeploymentsReturnsErrorForCorruptedFiles(t *testing.T) {
	store := newTestStore(t)
	deployment := newTestDeployment("app", "host-1")
	if err := store.SaveDeployment(deployment); err != nil {
		t.Fatalf("SaveDeployment: %v", err)
	}

	corruptedPath := filepath.Join(store.dataPath, "history", "runs", "corrupted.json")
	if err := os.WriteFile(corruptedPath, []byte("{invalid json"), 0644); err != nil {
		t.Fatalf("write corrupted file: %v", err)
	}

	deployments, err := store.ListDeployments("", "", 0)
	if err == nil {
		t.Fatal("ListDeployments should return error when a run file is corrupted")
	}
	if len(deployments) != 1 {
		t.Errorf("expected 1 valid deployment alongside error, got %d", len(deployments))
	}
}

// ListLatestDeployments

func TestListLatestDeployments(t *testing.T) {
	store := newTestStore(t)

	projectHostPairs := [][2]string{
		{"app-a", "host-1"},
		{"app-a", "host-2"},
		{"app-b", "host-1"},
	}
	for _, pair := range projectHostPairs {
		if err := store.SaveDeployment(newTestDeployment(pair[0], pair[1])); err != nil {
			t.Fatalf("save %v: %v", pair, err)
		}
	}

	deployments, err := store.ListLatestDeployments()
	if err != nil {
		t.Fatalf("ListLatestDeployments: %v", err)
	}
	if len(deployments) != 3 {
		t.Errorf("count: got %d, want 3", len(deployments))
	}
	if deployments[0].Project != "app-a" || deployments[0].Host != "host-1" {
		t.Errorf("first entry: got %s/%s, want app-a/host-1", deployments[0].Project, deployments[0].Host)
	}
}

func TestListLatestDeploymentsEmpty(t *testing.T) {
	store := newTestStore(t)
	deployments, err := store.ListLatestDeployments()
	if err != nil {
		t.Fatalf("ListLatestDeployments on empty store: %v", err)
	}
	if len(deployments) != 0 {
		t.Errorf("expected empty slice, got %v", deployments)
	}
}

// Repeated saves for one (project, host) pair collapse to a single index entry.
func TestListLatestDeploymentsOnePerProjectHostPair(t *testing.T) {
	store := newTestStore(t)

	baseTime := time.Now().UTC()
	for i := range 5 {
		deployment := newTestDeployment("app", "host-1")
		deployment.StartedAt = baseTime.Add(time.Duration(i) * time.Second)
		if err := store.SaveDeployment(deployment); err != nil {
			t.Fatalf("SaveDeployment %d: %v", i, err)
		}
	}

	deployments, err := store.ListLatestDeployments()
	if err != nil {
		t.Fatalf("ListLatestDeployments: %v", err)
	}
	if len(deployments) != 1 {
		t.Errorf("expected 1 entry for same project/host, got %d", len(deployments))
	}
}

// MarkStaleRunningDeploymentsFailed

func TestMarkStaleRunningDeploymentsFailed_NoneRunning(t *testing.T) {
	store := newTestStore(t)

	deployment := newTestDeployment("app", "host-1")
	deployment.Status = StatusSuccess
	if err := store.SaveDeployment(deployment); err != nil {
		t.Fatalf("SaveDeployment: %v", err)
	}

	count, err := store.MarkStaleRunningDeploymentsFailed()
	if err != nil {
		t.Fatalf("MarkStaleRunningDeploymentsFailed: %v", err)
	}
	if count != 0 {
		t.Errorf("count: got %d, want 0", count)
	}

	latest, err := store.GetLatestDeployment("app", "host-1")
	if err != nil {
		t.Fatalf("GetLatestDeployment: %v", err)
	}
	if latest.Status != StatusSuccess {
		t.Errorf("Status should be unchanged: got %q, want success", latest.Status)
	}
}

func TestMarkStaleRunningDeploymentsFailed_EmptyStore(t *testing.T) {
	store := newTestStore(t)
	count, err := store.MarkStaleRunningDeploymentsFailed()
	if err != nil {
		t.Fatalf("MarkStaleRunningDeploymentsFailed on empty store: %v", err)
	}
	if count != 0 {
		t.Errorf("count: got %d, want 0", count)
	}
}

func TestMarkStaleRunningDeploymentsFailed_MarksRunningAsFailed(t *testing.T) {
	store := newTestStore(t)

	running := newTestDeployment("app", "host-1")
	running.Status = StatusRunning
	if err := store.SaveDeployment(running); err != nil {
		t.Fatalf("SaveDeployment running: %v", err)
	}

	success := newTestDeployment("other", "host-1")
	success.Status = StatusSuccess
	if err := store.SaveDeployment(success); err != nil {
		t.Fatalf("SaveDeployment success: %v", err)
	}

	count, err := store.MarkStaleRunningDeploymentsFailed()
	if err != nil {
		t.Fatalf("MarkStaleRunningDeploymentsFailed: %v", err)
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}

	latest, err := store.GetLatestDeployment("app", "host-1")
	if err != nil {
		t.Fatalf("GetLatestDeployment: %v", err)
	}
	if latest.Status != StatusFailed {
		t.Errorf("index status: got %q, want failed", latest.Status)
	}
	if latest.Error == "" {
		t.Error("interrupted deployment should have an error message")
	}
	if latest.CompletedAt == nil {
		t.Error("CompletedAt should be set on interrupted deployment")
	}

	retrieved, err := store.GetLatestDeployment(running.Project, running.Host)
	if err != nil {
		t.Fatalf("GetLatestDeployment: %v", err)
	}
	if retrieved.Status != StatusFailed {
		t.Errorf("run file status: got %q, want failed", retrieved.Status)
	}

	successLatest, err := store.GetLatestDeployment("other", "host-1")
	if err != nil {
		t.Fatalf("GetLatestDeployment(other): %v", err)
	}
	if successLatest.Status != StatusSuccess {
		t.Errorf("non-running deployment should be unchanged: got %q", successLatest.Status)
	}
}

func TestMarkStaleRunningDeploymentsFailed_MultiplePairs(t *testing.T) {
	store := newTestStore(t)

	pairs := [][2]string{{"app-a", "host-1"}, {"app-b", "host-1"}, {"app-c", "host-2"}}
	for _, pair := range pairs {
		deployment := newTestDeployment(pair[0], pair[1])
		deployment.Status = StatusRunning
		if err := store.SaveDeployment(deployment); err != nil {
			t.Fatalf("SaveDeployment %v: %v", pair, err)
		}
	}

	count, err := store.MarkStaleRunningDeploymentsFailed()
	if err != nil {
		t.Fatalf("MarkStaleRunningDeploymentsFailed: %v", err)
	}
	if count != 3 {
		t.Errorf("count: got %d, want 3", count)
	}

	for _, pair := range pairs {
		latest, err := store.GetLatestDeployment(pair[0], pair[1])
		if err != nil {
			t.Fatalf("GetLatestDeployment(%v): %v", pair, err)
		}
		if latest.Status != StatusFailed {
			t.Errorf("(%s/%s) status: got %q, want failed", pair[0], pair[1], latest.Status)
		}
	}
}

func TestMarkStaleRunningDeploymentsFailed_Idempotent(t *testing.T) {
	store := newTestStore(t)

	deployment := newTestDeployment("app", "host-1")
	deployment.Status = StatusRunning
	if err := store.SaveDeployment(deployment); err != nil {
		t.Fatalf("SaveDeployment: %v", err)
	}

	if _, err := store.MarkStaleRunningDeploymentsFailed(); err != nil {
		t.Fatalf("first call: %v", err)
	}
	count, err := store.MarkStaleRunningDeploymentsFailed()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if count != 0 {
		t.Errorf("second call should find nothing to update, got count=%d", count)
	}
}

// RepoState

func TestSaveAndGetRepoState(t *testing.T) {
	store := newTestStore(t)
	repoState := &RepoState{
		Repo:        "owner/repo",
		Branch:      "main",
		LastSeenSha: "abc123",
	}

	if err := store.SaveRepoState(repoState); err != nil {
		t.Fatalf("SaveRepoState: %v", err)
	}

	retrieved, err := store.GetRepoState("owner/repo", "main")
	if err != nil {
		t.Fatalf("GetRepoState: %v", err)
	}
	if retrieved == nil {
		t.Fatal("GetRepoState returned nil")
	}
	if retrieved.LastSeenSha != "abc123" {
		t.Errorf("LastSeenSha: got %q, want abc123", retrieved.LastSeenSha)
	}
	if retrieved.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set after save")
	}
}

func TestGetRepoStateNotFound(t *testing.T) {
	store := newTestStore(t)
	retrieved, err := store.GetRepoState("nonexistent/repo", "main")
	if err != nil {
		t.Fatalf("GetRepoState on empty store: %v", err)
	}
	if retrieved != nil {
		t.Errorf("expected nil for unknown repo/branch, got %v", retrieved)
	}
}

// SaveRepoState must not mutate the caller's struct (UpdatedAt goes on a copy).
func TestSaveRepoStateDoesNotMutateCallerStruct(t *testing.T) {
	store := newTestStore(t)
	repoState := &RepoState{
		Repo:   "owner/repo",
		Branch: "main",
	}
	originalUpdatedAt := repoState.UpdatedAt

	if err := store.SaveRepoState(repoState); err != nil {
		t.Fatalf("SaveRepoState: %v", err)
	}

	if !repoState.UpdatedAt.Equal(originalUpdatedAt) {
		t.Errorf("SaveRepoState mutated caller's UpdatedAt: got %v, want zero value", repoState.UpdatedAt)
	}
}

// Repo names differing only by "/" vs "_" must not map to the same file.
func TestRepoStatePathCollisionSlashVsUnderscore(t *testing.T) {
	store := newTestStore(t)

	slashRepo := &RepoState{Repo: "owner/repo", Branch: "main", LastSeenSha: "aaaa"}
	underscoreRepo := &RepoState{Repo: "owner_repo", Branch: "main", LastSeenSha: "bbbb"}

	if err := store.SaveRepoState(slashRepo); err != nil {
		t.Fatalf("save slash repo: %v", err)
	}
	if err := store.SaveRepoState(underscoreRepo); err != nil {
		t.Fatalf("save underscore repo: %v", err)
	}

	retrievedSlash, err := store.GetRepoState("owner/repo", "main")
	if err != nil {
		t.Fatalf("GetRepoState(owner/repo): %v", err)
	}
	if retrievedSlash == nil || retrievedSlash.LastSeenSha != "aaaa" {
		t.Errorf("owner/repo state corrupted: expected SHA aaaa, got %v", retrievedSlash)
	}

	retrievedUnderscore, err := store.GetRepoState("owner_repo", "main")
	if err != nil {
		t.Fatalf("GetRepoState(owner_repo): %v", err)
	}
	if retrievedUnderscore == nil || retrievedUnderscore.LastSeenSha != "bbbb" {
		t.Errorf("owner_repo state corrupted: expected SHA bbbb, got %v", retrievedUnderscore)
	}
}

// Repo/branch names containing "-" must not produce ambiguous filenames.
func TestRepoStatePathCollisionDashSeparator(t *testing.T) {
	store := newTestStore(t)

	repoWithDashBranch := &RepoState{Repo: "a", Branch: "b-c", LastSeenSha: "1111"}
	dashRepoWithBranch := &RepoState{Repo: "a-b", Branch: "c", LastSeenSha: "2222"}

	if err := store.SaveRepoState(repoWithDashBranch); err != nil {
		t.Fatalf("save a/b-c: %v", err)
	}
	if err := store.SaveRepoState(dashRepoWithBranch); err != nil {
		t.Fatalf("save a-b/c: %v", err)
	}

	retrieved, err := store.GetRepoState("a", "b-c")
	if err != nil {
		t.Fatalf("GetRepoState(a, b-c): %v", err)
	}
	if retrieved == nil || retrieved.LastSeenSha != "1111" {
		t.Errorf("repo=a branch=b-c state corrupted: expected SHA 1111, got %v", retrieved)
	}
}

func TestRepoStateDifferentBranchesDontCollide(t *testing.T) {
	store := newTestStore(t)

	mainState := &RepoState{Repo: "owner/repo", Branch: "main", LastSeenSha: "aaaa"}
	devState := &RepoState{Repo: "owner/repo", Branch: "dev", LastSeenSha: "bbbb"}
	featureState := &RepoState{Repo: "owner/repo", Branch: "feature/new-ui", LastSeenSha: "cccc"}

	for _, state := range []*RepoState{mainState, devState, featureState} {
		if err := store.SaveRepoState(state); err != nil {
			t.Fatalf("SaveRepoState(%s): %v", state.Branch, err)
		}
	}

	retrievedMain, err := store.GetRepoState("owner/repo", "main")
	if err != nil || retrievedMain == nil || retrievedMain.LastSeenSha != "aaaa" {
		t.Errorf("main state wrong: %v / %v", err, retrievedMain)
	}

	retrievedDev, err := store.GetRepoState("owner/repo", "dev")
	if err != nil || retrievedDev == nil || retrievedDev.LastSeenSha != "bbbb" {
		t.Errorf("dev state wrong: %v / %v", err, retrievedDev)
	}

	retrievedFeature, err := store.GetRepoState("owner/repo", "feature/new-ui")
	if err != nil || retrievedFeature == nil || retrievedFeature.LastSeenSha != "cccc" {
		t.Errorf("feature/new-ui state wrong: %v / %v", err, retrievedFeature)
	}
}

// Atomic write correctness

func TestAtomicWriteNoPartialRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	type payload struct {
		Value int `json:"value"`
	}

	const iterations = 100
	for i := range iterations {
		if err := writeJSON(path, payload{Value: i}); err != nil {
			t.Fatalf("writeJSON iter %d: %v", i, err)
		}
		var p payload
		if err := readJSON(path, &p); err != nil {
			t.Fatalf("readJSON iter %d: %v", i, err)
		}
	}
}

// Concurrent correctness

// Concurrent saves: no errors, valid JSON, one index entry per pair, all run files present.
func TestConcurrentSave(t *testing.T) {
	store := newTestStore(t)

	const workers = 10
	const deploysPerWorker = 20

	var wg sync.WaitGroup
	errCh := make(chan error, workers*deploysPerWorker)

	for workerIndex := range workers {
		wg.Add(1)
		go func(workerIndex int) {
			defer wg.Done()
			project := fmt.Sprintf("project-%02d", workerIndex)
			for range deploysPerWorker {
				deployment := newTestDeployment(project, "host-1")
				if err := store.SaveDeployment(deployment); err != nil {
					errCh <- fmt.Errorf("worker %d: %w", workerIndex, err)
				}
			}
		}(workerIndex)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent save error: %v", err)
	}

	projectsFilePath := filepath.Join(store.dataPath, "state", "projects.json")
	rawContent, err := os.ReadFile(projectsFilePath)
	if err != nil {
		t.Fatalf("read projects.json: %v", err)
	}
	var latestDeployments map[string]*Deployment
	if err := json.Unmarshal(rawContent, &latestDeployments); err != nil {
		t.Fatalf("projects.json is corrupt: %v\ncontent: %s", err, rawContent)
	}

	if len(latestDeployments) != workers {
		t.Errorf("projects.json entries: got %d, want %d", len(latestDeployments), workers)
	}

	runsDirectoryPath := filepath.Join(store.dataPath, "history", "runs")
	dirEntries, err := os.ReadDir(runsDirectoryPath)
	if err != nil {
		t.Fatalf("read runs dir: %v", err)
	}
	if len(dirEntries) != workers*deploysPerWorker {
		t.Errorf("run files: got %d, want %d", len(dirEntries), workers*deploysPerWorker)
	}
}

// Concurrent saves across pairs must not cross-contaminate the index.
func TestConcurrentMixedProjectHost(t *testing.T) {
	store := newTestStore(t)

	projectHostPairs := [][2]string{
		{"app-a", "host-1"},
		{"app-a", "host-2"},
		{"app-b", "host-1"},
		{"app-b", "host-2"},
	}

	const deploysPerPair = 15
	var wg sync.WaitGroup
	errCh := make(chan error, len(projectHostPairs)*deploysPerPair)

	for _, pair := range projectHostPairs {
		wg.Add(1)
		go func(project, host string) {
			defer wg.Done()
			for range deploysPerPair {
				deployment := newTestDeployment(project, host)
				if err := store.SaveDeployment(deployment); err != nil {
					errCh <- fmt.Errorf("%s/%s: %w", project, host, err)
				}
			}
		}(pair[0], pair[1])
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent error: %v", err)
	}

	deployments, err := store.ListLatestDeployments()
	if err != nil {
		t.Fatalf("ListLatestDeployments: %v", err)
	}
	if len(deployments) != len(projectHostPairs) {
		t.Errorf("ListLatestDeployments count: got %d, want %d", len(deployments), len(projectHostPairs))
	}

	seen := make(map[string]bool)
	for _, deployment := range deployments {
		key := deployment.Project + "/" + deployment.Host
		if seen[key] {
			t.Errorf("duplicate entry for %q in ListLatestDeployments", key)
		}
		seen[key] = true
	}
}

// NewID

func TestNewIDUniqueness(t *testing.T) {
	ids := make(map[string]bool)
	for range 100 {
		id := NewID()
		if id == "" {
			t.Fatal("NewID returned empty string")
		}
		if ids[id] {
			t.Errorf("duplicate ID: %q", id)
		}
		ids[id] = true
	}
}

func TestNewIDFormat(t *testing.T) {
	id := NewID()
	parts := strings.SplitN(id, "-", 2)
	if len(parts) != 2 {
		t.Fatalf("NewID format invalid: expected <timestamp>-<hex>, got %q", id)
	}
	if len(parts[1]) != 8 {
		t.Errorf("NewID hex suffix: expected 8 chars, got %d in %q", len(parts[1]), id)
	}
}

// Nil input should be rejected instead of panicking.
func TestSaveDeploymentNil(t *testing.T) {
	store := newTestStore(t)

	if err := store.SaveDeployment(nil); err == nil {
		t.Fatal("SaveDeployment should return an error for a nil deployment")
	}
}

// Nil input should be rejected instead of panicking.
func TestSaveRepoStateNil(t *testing.T) {
	store := newTestStore(t)

	if err := store.SaveRepoState(nil); err == nil {
		t.Fatal("SaveRepoState should return an error for a nil repo state")
	}
}

// Invalid deployment files should be reported while valid entries are still returned.
func TestListDeploymentsInvalidRecord(t *testing.T) {
	store := newTestStore(t)
	validDeployment := newTestDeployment("app", "host-1")
	if err := store.SaveDeployment(validDeployment); err != nil {
		t.Fatalf("SaveDeployment: %v", err)
	}

	invalidDeploymentPath := filepath.Join(store.dataPath, "history", "runs", "invalid.json")
	if err := os.WriteFile(invalidDeploymentPath, []byte(`{"status":"success"}`), 0644); err != nil {
		t.Fatalf("write invalid deployment file: %v", err)
	}

	deployments, err := store.ListDeployments("", "", 0)
	if err == nil {
		t.Fatal("ListDeployments should return an error for an invalid deployment record")
	}
	if len(deployments) != 1 {
		t.Fatalf("expected 1 valid deployment alongside the error, got %d", len(deployments))
	}
	if deployments[0].ID != validDeployment.ID {
		t.Errorf("expected valid deployment %q, got %q", validDeployment.ID, deployments[0].ID)
	}
}
