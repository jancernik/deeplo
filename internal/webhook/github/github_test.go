package github_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/jancernik/deeplo/internal/webhook"
	githubwebhook "github.com/jancernik/deeplo/internal/webhook/github"
)

// helpers

const testSecret = "supersecret"

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// writeSecretFile writes secret to a temp file and returns its path.
func writeSecretFile(t *testing.T, secret string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "webhook_secret")
	if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	return path
}

// buildPushBody returns a minimal GitHub push payload JSON.
func buildPushBody(t *testing.T, ref, after, repo string, commits []githubwebhook.Commit) []byte {
	t.Helper()
	payload := githubwebhook.PushPayload{
		Ref:   ref,
		After: after,
		Repository: githubwebhook.Repository{
			FullName: repo,
		},
		Commits: commits,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal push payload: %v", err)
	}
	return b
}

// newHandler creates a Handler backed by a temp secret file.
func newHandler(t *testing.T, secret string, onPush func(context.Context, webhook.PushEvent)) *githubwebhook.Handler {
	t.Helper()
	var secretFile string
	if secret != "" {
		secretFile = writeSecretFile(t, secret)
	}
	h, err := githubwebhook.NewHandler(context.Background(), secretFile, nil, onPush, slog.Default())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func postWebhook(t *testing.T, h http.Handler, body []byte, sig, event, deliveryID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if deliveryID != "" {
		req.Header.Set("X-GitHub-Delivery", deliveryID)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// Handler

func TestHandler_MethodNotAllowed(t *testing.T) {
	h := newHandler(t, "", func(_ context.Context, _ webhook.PushEvent) {})
	req := httptest.NewRequest(http.MethodGet, "/webhooks/github", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want 405", rr.Code)
	}
}

func TestHandler_MissingSignature_Rejected(t *testing.T) {
	h := newHandler(t, testSecret, func(_ context.Context, _ webhook.PushEvent) {})
	body := buildPushBody(t, "refs/heads/main", "abc", "owner/repo", nil)
	rr := postWebhook(t, h, body, "", "push", "d-001")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rr.Code)
	}
}

func TestHandler_InvalidSignature_Rejected(t *testing.T) {
	h := newHandler(t, testSecret, func(_ context.Context, _ webhook.PushEvent) {})
	body := buildPushBody(t, "refs/heads/main", "abc", "owner/repo", nil)
	rr := postWebhook(t, h, body, "sha256=badc0ffee", "push", "d-002")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rr.Code)
	}
}

func TestHandler_WrongSecretSignature_Rejected(t *testing.T) {
	h := newHandler(t, testSecret, func(_ context.Context, _ webhook.PushEvent) {})
	body := buildPushBody(t, "refs/heads/main", "abc", "owner/repo", nil)
	sig := signBody("wrongsecret", body)
	rr := postWebhook(t, h, body, sig, "push", "d-003")
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rr.Code)
	}
}

func TestHandler_ValidSignature_Accepted(t *testing.T) {
	received := make(chan webhook.PushEvent, 1)
	h := newHandler(t, testSecret, func(_ context.Context, push webhook.PushEvent) {
		received <- push
	})
	body := buildPushBody(t, "refs/heads/main", "deadbeef", "owner/repo", nil)
	sig := signBody(testSecret, body)
	rr := postWebhook(t, h, body, sig, "push", "d-004")
	if rr.Code != http.StatusAccepted {
		t.Errorf("got %d, want 202", rr.Code)
	}
	select {
	case push := <-received:
		if push.Branch != "main" {
			t.Errorf("branch: got %q, want main", push.Branch)
		}
	case <-time.After(time.Second):
		t.Error("onPush was not called within 1s")
	}
}

func TestHandler_NoSecretConfigured_NoValidation(t *testing.T) {
	received := make(chan struct{}, 1)
	h := newHandler(t, "", func(_ context.Context, _ webhook.PushEvent) {
		received <- struct{}{}
	})
	body := buildPushBody(t, "refs/heads/main", "abc", "owner/repo", nil)
	// No signature header - should still be accepted when no secret is configured.
	rr := postWebhook(t, h, body, "", "push", "d-005")
	if rr.Code != http.StatusAccepted {
		t.Errorf("got %d, want 202", rr.Code)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Error("onPush was not called")
	}
}

func TestHandler_NonPushEvent_NoContent(t *testing.T) {
	h := newHandler(t, "", func(_ context.Context, _ webhook.PushEvent) {
		panic("onPush should not be called for non-push event")
	})
	body := []byte(`{"action":"opened"}`)
	rr := postWebhook(t, h, body, "", "pull_request", "d-006")
	if rr.Code != http.StatusNoContent {
		t.Errorf("got %d, want 204", rr.Code)
	}
}

func TestHandler_DuplicateDelivery_IgnoredSecond(t *testing.T) {
	var callCount int
	var mu sync.Mutex
	firstDone := make(chan struct{})
	h := newHandler(t, "", func(_ context.Context, _ webhook.PushEvent) {
		mu.Lock()
		callCount++
		mu.Unlock()
		close(firstDone)
	})
	body := buildPushBody(t, "refs/heads/main", "abc", "owner/repo", nil)

	rr1 := postWebhook(t, h, body, "", "push", "d-dup")
	if rr1.Code != http.StatusAccepted {
		t.Errorf("first delivery: got %d, want 202", rr1.Code)
	}
	// The dedup Seen() check is synchronous, so rr2 is independent of the goroutine.
	rr2 := postWebhook(t, h, body, "", "push", "d-dup")
	if rr2.Code != http.StatusNoContent {
		t.Errorf("duplicate delivery: got %d, want 204", rr2.Code)
	}

	// Wait for the goroutine spawned by the first delivery before checking callCount.
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("onPush not called for first delivery")
	}
	mu.Lock()
	if callCount != 1 {
		t.Errorf("onPush called %d times, want 1", callCount)
	}
	mu.Unlock()
}

func TestHandler_ParsedPushEvent(t *testing.T) {
	received := make(chan webhook.PushEvent, 1)
	h := newHandler(t, "", func(_ context.Context, push webhook.PushEvent) {
		received <- push
	})

	commits := []githubwebhook.Commit{
		{Added: []string{"services/web/main.go"}, Modified: []string{"compose.yaml"}},
		{Modified: []string{"services/web/handler.go"}, Removed: []string{"old.go"}},
	}
	body := buildPushBody(t, "refs/heads/feature/x", "cafebabe", "acme/myrepo", commits)
	postWebhook(t, h, body, "", "push", "d-007")

	select {
	case push := <-received:
		if push.Branch != "feature/x" {
			t.Errorf("branch: got %q, want feature/x", push.Branch)
		}
		if push.CommitSha != "cafebabe" {
			t.Errorf("sha: got %q", push.CommitSha)
		}
		if push.RepoFullName != "acme/myrepo" {
			t.Errorf("repo: got %q", push.RepoFullName)
		}
		want := []string{"services/web/main.go", "compose.yaml", "services/web/handler.go", "old.go"}
		if len(push.ChangedFiles) != len(want) {
			t.Errorf("files: got %v, want %v", push.ChangedFiles, want)
		} else {
			for i, f := range want {
				if push.ChangedFiles[i] != f {
					t.Errorf("file[%d]: got %q, want %q", i, push.ChangedFiles[i], f)
				}
			}
		}
	case <-time.After(time.Second):
		t.Error("onPush was not called")
	}
}

// ParsePushPayload

func TestParsePushPayload(t *testing.T) {
	cases := []struct {
		name       string
		json       string
		wantBranch string
		wantSHA    string
		wantRepo   string
		wantFiles  []string
	}{
		{
			name: "main branch push",
			json: `{
				"ref": "refs/heads/main",
				"after": "abc123",
				"repository": {"full_name": "owner/repo"},
				"commits": [
					{"added": ["a.go"], "modified": ["b.go"], "removed": []}
				]
			}`,
			wantBranch: "main",
			wantSHA:    "abc123",
			wantRepo:   "owner/repo",
			wantFiles:  []string{"a.go", "b.go"},
		},
		{
			name: "feature branch push",
			json: `{
				"ref": "refs/heads/feature/my-feature",
				"after": "deadbeef",
				"repository": {"full_name": "org/proj"},
				"commits": []
			}`,
			wantBranch: "feature/my-feature",
			wantSHA:    "deadbeef",
			wantRepo:   "org/proj",
			wantFiles:  nil,
		},
		{
			name: "deduplication across commits",
			json: `{
				"ref": "refs/heads/main",
				"after": "aaa",
				"repository": {"full_name": "x/y"},
				"commits": [
					{"added": ["file.go"], "modified": ["file.go"]},
					{"modified": ["file.go", "other.go"]}
				]
			}`,
			wantBranch: "main",
			wantSHA:    "aaa",
			wantRepo:   "x/y",
			wantFiles:  []string{"file.go", "other.go"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			p, err := githubwebhook.ParsePushPayload([]byte(testCase.json))
			if err != nil {
				t.Fatalf("ParsePushPayload: %v", err)
			}
			if p.Branch() != testCase.wantBranch {
				t.Errorf("branch: got %q, want %q", p.Branch(), testCase.wantBranch)
			}
			if p.After != testCase.wantSHA {
				t.Errorf("sha: got %q, want %q", p.After, testCase.wantSHA)
			}
			if p.Repository.FullName != testCase.wantRepo {
				t.Errorf("repo: got %q, want %q", p.Repository.FullName, testCase.wantRepo)
			}
			files := p.ChangedFiles()
			if len(files) != len(testCase.wantFiles) {
				t.Errorf("files: got %v, want %v", files, testCase.wantFiles)
			} else {
				for i, f := range testCase.wantFiles {
					if files[i] != f {
						t.Errorf("files[%d]: got %q, want %q", i, files[i], f)
					}
				}
			}
		})
	}
}

// Regression: a push with no file changes (empty commit, bare branch push) must
// yield an empty non-nil slice. A nil slice reads as "unknown diff" to the planner,
// which deploys every target.
func TestChangedFiles_EmptyPush_NotNil(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"no commits", `{"ref": "refs/heads/main", "after": "abc", "repository": {"full_name": "x/y"}, "commits": []}`},
		{"commit with no files", `{"ref": "refs/heads/main", "after": "abc", "repository": {"full_name": "x/y"}, "commits": [{"added": [], "modified": [], "removed": []}]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			p, err := githubwebhook.ParsePushPayload([]byte(testCase.json))
			if err != nil {
				t.Fatalf("ParsePushPayload: %v", err)
			}
			files := p.ChangedFiles()
			if files == nil {
				t.Error("ChangedFiles: got nil, want empty non-nil slice")
			}
			if len(files) != 0 {
				t.Errorf("ChangedFiles: got %v, want empty", files)
			}
		})
	}
}

func TestParsePushPayload_InvalidJSON(t *testing.T) {
	_, err := githubwebhook.ParsePushPayload([]byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// DedupeCache

func TestDedupeCache_NewID_NotSeen(t *testing.T) {
	d := githubwebhook.NewDedupeCache(10)
	if d.Seen("id-1") {
		t.Error("first call should return false (not seen)")
	}
}

func TestDedupeCache_SameID_Seen(t *testing.T) {
	d := githubwebhook.NewDedupeCache(10)
	d.Seen("id-1")
	if !d.Seen("id-1") {
		t.Error("second call with same id should return true (seen)")
	}
}

func TestDedupeCache_CapacityEviction(t *testing.T) {
	d := githubwebhook.NewDedupeCache(3)
	d.Seen("a")
	d.Seen("b")
	d.Seen("c")
	// Adding "d" evicts "a" (oldest).
	d.Seen("d")
	// "a" should no longer be in the cache.
	if d.Seen("a") {
		t.Error("'a' should have been evicted")
	}
	// "b", "c", "d" are still recent (depending on eviction order after re-adding "a")
}

func TestDedupeCache_DifferentIDs(t *testing.T) {
	d := githubwebhook.NewDedupeCache(100)
	ids := []string{"alpha", "beta", "gamma"}
	for _, id := range ids {
		if d.Seen(id) {
			t.Errorf("first Seen(%q) should be false", id)
		}
	}
	for _, id := range ids {
		if !d.Seen(id) {
			t.Errorf("second Seen(%q) should be true", id)
		}
	}
}
