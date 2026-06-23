package runlog_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jancernik/deeplo/internal/runlog"
)

// RunLog creation

func TestOpen_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	rl, err := runlog.Open(dir, "1744660000-aabbccdd")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rl.Close() //nolint:errcheck

	path := filepath.Join(dir, "1744660000-aabbccdd.log")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected log file at %s: %v", path, err)
	}
}

func TestOpen_CreatesDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "runs")
	rl, err := runlog.Open(dir, "1744660000-aabbccdd")
	if err != nil {
		t.Fatalf("Open with nested dir: %v", err)
	}
	defer rl.Close() //nolint:errcheck

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected nested dir to be created: %v", err)
	}
}

// nil safety

func TestRunLog_NilSafe(t *testing.T) {
	// All methods on a nil *RunLog must not panic.
	var rl *runlog.RunLog
	rl.Println("test")
	rl.Logf("test %d", 1)
	if err := rl.Close(); err != nil {
		t.Errorf("nil Close: %v", err)
	}
}

// write content

func TestRunLog_Println_WritesLine(t *testing.T) {
	dir := t.TempDir()
	rl, _ := runlog.Open(dir, "1744660000-aabbccdd")

	rl.Println("hello world")
	_ = rl.Close()

	data, _ := os.ReadFile(filepath.Join(dir, "1744660000-aabbccdd.log"))
	if !strings.Contains(string(data), "hello world\n") {
		t.Errorf("expected 'hello world' in log, got: %q", string(data))
	}
}

func TestRunLog_Logf_ContainsTimestampAndMessage(t *testing.T) {
	dir := t.TempDir()
	rl, _ := runlog.Open(dir, "1744660000-aabbccdd")

	rl.Logf("step %d complete", 3)
	_ = rl.Close()

	data, _ := os.ReadFile(filepath.Join(dir, "1744660000-aabbccdd.log"))
	content := string(data)

	// Timestamp format: [HH:MM:SSZ]
	if !strings.Contains(content, "Z] step 3 complete") {
		t.Errorf("expected timestamped message in log, got: %q", content)
	}
}

// HTTP handler

func writeLog(t *testing.T, dir, id, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".log"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestHandler_ServesLog(t *testing.T) {
	dir := t.TempDir()
	id := "1744660000-aabbccdd"
	writeLog(t, dir, id, "line one\nline two\n")

	h := runlog.Handler(dir)
	req := httptest.NewRequest(http.MethodGet, "/runs/"+id+"/logs", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), "line one") {
		t.Errorf("expected log content in response, got: %q", string(body))
	}
}

func TestHandler_ContentType(t *testing.T) {
	dir := t.TempDir()
	id := "1744660000-aabbccdd"
	writeLog(t, dir, id, "content")

	h := runlog.Handler(dir)
	req := httptest.NewRequest(http.MethodGet, "/runs/"+id+"/logs", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type: got %q, want %q", ct, "text/plain; charset=utf-8")
	}
}

func TestHandler_MissingRun_Returns404(t *testing.T) {
	dir := t.TempDir()
	h := runlog.Handler(dir)

	req := httptest.NewRequest(http.MethodGet, "/runs/1744660000-aabbccdd/logs", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestHandler_MalformedPath_Returns404(t *testing.T) {
	dir := t.TempDir()
	h := runlog.Handler(dir)

	cases := []string{
		"/runs/",
		"/runs/noext",
		"/runs/id/notlogs",
		"/runs/id/logs/extra",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Errorf("path %q: status %d, want 404", path, w.Code)
			}
		})
	}
}

func TestHandler_PathTraversal_Returns404(t *testing.T) {
	dir := t.TempDir()
	// Place a file that path traversal would reach.
	if err := os.WriteFile(filepath.Join(dir, "secret.log"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	h := runlog.Handler(dir)
	cases := []string{
		"/runs/../secret/logs",
		"/runs/%2e%2e%2fsecret/logs",
		"/runs/../../etc/passwd/logs",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Errorf("path %q: status %d, want 404 (path traversal should be rejected)", path, w.Code)
			}
		})
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	dir := t.TempDir()
	id := "1744660000-aabbccdd"
	writeLog(t, dir, id, "content")

	h := runlog.Handler(dir)
	req := httptest.NewRequest(http.MethodPost, "/runs/"+id+"/logs", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

func TestHandler_HeadRequest(t *testing.T) {
	dir := t.TempDir()
	id := "1744660000-aabbccdd"
	writeLog(t, dir, id, "content")

	h := runlog.Handler(dir)
	req := httptest.NewRequest(http.MethodHead, "/runs/"+id+"/logs", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	// HEAD must have correct Content-Type but no body.
	if w.Body.Len() != 0 {
		t.Errorf("HEAD response should have empty body, got %d bytes", w.Body.Len())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type: got %q", ct)
	}
}
