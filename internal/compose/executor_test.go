package compose_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jancernik/deeplo/internal/compose"
)

// mock ssh.Connection

type responder struct {
	match  string // substring matched against the command
	stdout string
	stderr string
	err    error
}

type mockConn struct {
	mu         sync.Mutex
	responders []responder
	runLog     []string // commands received, in order
	uploadLog  []string // "<localPath> → <remotePath>"
}

func (mock *mockConn) Run(_ context.Context, command string) (string, string, error) {
	mock.mu.Lock()
	mock.runLog = append(mock.runLog, command)
	mock.mu.Unlock()
	for _, response := range mock.responders {
		if strings.Contains(command, response.match) {
			return response.stdout, response.stderr, response.err
		}
	}
	return "", "", nil
}

func (mock *mockConn) Upload(_ context.Context, localPath, remotePath string) error {
	mock.mu.Lock()
	mock.uploadLog = append(mock.uploadLog, localPath+" → "+remotePath)
	mock.mu.Unlock()
	return nil
}

func (mock *mockConn) Close() error { return nil }

// helpers

func newExec(t *testing.T, conn *mockConn, remoteDir string) *compose.Executor {
	t.Helper()
	return compose.NewExecutor(conn, remoteDir, "myapp", slog.Default())
}

// makeBundle creates a bundle with real local temp files.
func makeBundle(t *testing.T, files map[string]string) *compose.Bundle {
	t.Helper()
	dir := t.TempDir()
	bundle := &compose.Bundle{}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write bundle file %q: %v", name, err)
		}
		bundle.Files = append(bundle.Files, compose.BundleFile{LocalPath: path, RemoteName: name})
	}
	return bundle
}

// shellQuote is tested indirectly via the commands

// Preflight

func TestPreflight_Success(t *testing.T) {
	conn := &mockConn{
		responders: []responder{
			{match: "config", stdout: "services: {}\n"},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	if err := executor.Preflight(context.Background(), "/srv/apps/myapp/.staging", []string{"compose.yaml"}); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if len(conn.runLog) != 1 || !strings.Contains(conn.runLog[0], "config") {
		t.Errorf("expected config command, got %v", conn.runLog)
	}
	if !strings.Contains(conn.runLog[0], "--project-name 'myapp'") {
		t.Errorf("expected stable project name flag, got %v", conn.runLog)
	}
}

func TestPreflight_Failure(t *testing.T) {
	conn := &mockConn{
		responders: []responder{
			{match: "config", stderr: "unknown key: foo", err: fmt.Errorf("exit status 1")},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	err := executor.Preflight(context.Background(), "/srv/apps/myapp/.staging", []string{"compose.yaml"})
	if err == nil {
		t.Fatal("expected error from failed preflight, got nil")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("error should contain stderr: %v", err)
	}
}

// Ps

func TestPs_ParsesJSON(t *testing.T) {
	psJSON := `{"Name":"app-test-1","Service":"test","State":"running","Status":"Up 3 minutes"}`
	conn := &mockConn{
		responders: []responder{
			{match: "ps", stdout: psJSON},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	services, err := executor.Ps(context.Background(), []string{"compose.yaml"})
	if err != nil {
		t.Fatalf("Ps: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("count: got %d, want 1", len(services))
	}
	if services[0].Service != "test" {
		t.Errorf("Service: got %q, want test", services[0].Service)
	}
	if services[0].State != "running" {
		t.Errorf("State: got %q, want running", services[0].State)
	}
}

func TestPs_EmptyReturnsNil(t *testing.T) {
	for _, out := range []string{"", "[]", "null"} {
		conn := &mockConn{responders: []responder{{match: "ps", stdout: out}}}
		executor := newExec(t, conn, "/srv/apps/myapp")
		services, err := executor.Ps(context.Background(), []string{"compose.yaml"})
		if err != nil {
			t.Fatalf("Ps(%q): %v", out, err)
		}
		if services != nil {
			t.Errorf("Ps(%q): expected nil, got %v", out, services)
		}
	}
}

// Deploy

func TestDeploy_HappyPath(t *testing.T) {
	psJSON := `{"Name":"app-web-1","Service":"web","State":"running","Status":"Up"}`
	conn := &mockConn{
		responders: []responder{
			{match: "mkdir"},
			{match: "config", stdout: "services: {}\n"},
			{match: "mv"},
			{match: "rm -rf"},
			{match: "up", stdout: ""},
			{match: "ps", stdout: psJSON},
		},
	}

	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{ComposeFiles: []string{"compose.yaml"}}

	result, err := executor.Deploy(context.Background(), bundle, opts)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(result.Services) != 1 {
		t.Errorf("services: got %d, want 1", len(result.Services))
	}

	// Verify command order: mkdir → config → mv → rm → up → ps
	expectSubstrings := []string{"mkdir", "config", "mv", "up", "ps"}
	cmdLog := strings.Join(conn.runLog, "|")
	for _, sub := range expectSubstrings {
		if !strings.Contains(cmdLog, sub) {
			t.Errorf("expected %q in command log, got: %v", sub, conn.runLog)
		}
	}
}

func TestDeploy_RuntimeCheckFailure_ExitedContainer(t *testing.T) {
	psJSON := `{"Name":"app-web-1","Service":"web","State":"exited","Status":"Exited (1) 2 seconds ago"}`
	conn := &mockConn{
		responders: []responder{
			{match: "mkdir"},
			{match: "config", stdout: "services: {}\n"},
			{match: "mv"},
			{match: "rm -rf"},
			{match: "up", stdout: ""},
			{match: "ps", stdout: psJSON},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{ComposeFiles: []string{"compose.yaml"}}

	_, err := executor.Deploy(context.Background(), bundle, opts)
	if err == nil {
		t.Fatal("expected error for exited container, got nil")
	}
	if !strings.Contains(err.Error(), "runtime check") {
		t.Errorf("error should mention 'runtime check': %v", err)
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("error should mention the bad state: %v", err)
	}
}

func TestDeploy_RuntimeCheckFailure_RestartingContainer(t *testing.T) {
	psJSON := `{"Name":"app-web-1","Service":"web","State":"restarting","Status":"Restarting (1) 1 second ago"}`
	conn := &mockConn{
		responders: []responder{
			{match: "mkdir"},
			{match: "config", stdout: "services: {}\n"},
			{match: "mv"},
			{match: "rm -rf"},
			{match: "up", stdout: ""},
			{match: "ps", stdout: psJSON},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{ComposeFiles: []string{"compose.yaml"}}

	_, err := executor.Deploy(context.Background(), bundle, opts)
	if err == nil {
		t.Fatal("expected error for restarting container, got nil")
	}
	if !strings.Contains(err.Error(), "runtime check") {
		t.Errorf("error should mention 'runtime check': %v", err)
	}
}

func TestDeploy_RuntimeCheckFailure_PsError(t *testing.T) {
	conn := &mockConn{
		responders: []responder{
			{match: "mkdir"},
			{match: "config", stdout: "services: {}\n"},
			{match: "mv"},
			{match: "rm -rf"},
			{match: "up", stdout: ""},
			{match: "ps", stderr: "permission denied", err: fmt.Errorf("exit 1")},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{ComposeFiles: []string{"compose.yaml"}}

	_, err := executor.Deploy(context.Background(), bundle, opts)
	if err == nil {
		t.Fatal("expected error when ps fails, got nil")
	}
	if !strings.Contains(err.Error(), "runtime check") {
		t.Errorf("error should mention 'runtime check': %v", err)
	}
}

func TestDeploy_RuntimeCheckFailure_ReturnsPartialResult(t *testing.T) {
	// Even on runtime check failure the result with service states is returned
	// so the caller can log which services are in a bad state.
	psJSON := `{"Name":"app-web-1","Service":"web","State":"exited","Status":"Exited (1)"}`
	conn := &mockConn{
		responders: []responder{
			{match: "mkdir"},
			{match: "config", stdout: "services: {}\n"},
			{match: "mv"},
			{match: "rm -rf"},
			{match: "up", stdout: "some output"},
			{match: "ps", stdout: psJSON},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{ComposeFiles: []string{"compose.yaml"}}

	result, err := executor.Deploy(context.Background(), bundle, opts)
	if err == nil {
		t.Fatal("expected error for exited container")
	}
	if result == nil {
		t.Fatal("result should be non-nil even on runtime check failure")
	}
	if len(result.Services) != 1 || result.Services[0].State != "exited" {
		t.Errorf("result.Services should carry the bad state, got: %+v", result.Services)
	}
	if result.ComposeOutput != "some output" {
		t.Errorf("result.ComposeOutput should be preserved, got: %q", result.ComposeOutput)
	}
}

func TestDeploy_PreflightFailureCleansStagingAndReturnsError(t *testing.T) {
	conn := &mockConn{
		responders: []responder{
			{match: "mkdir"},
			{match: "config", stderr: "invalid yaml", err: fmt.Errorf("exit 1")},
			{match: "rm -rf"},
		},
	}

	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "bad: yaml:"})
	opts := compose.DeployOptions{ComposeFiles: []string{"compose.yaml"}}

	_, err := executor.Deploy(context.Background(), bundle, opts)
	if err == nil {
		t.Fatal("expected error from failed preflight, got nil")
	}
	if !strings.Contains(err.Error(), "preflight") {
		t.Errorf("error should mention preflight: %v", err)
	}

	// rm -rf .staging must have been called
	cleanupCalled := false
	for _, command := range conn.runLog {
		if strings.Contains(command, "rm -rf") && strings.Contains(command, ".staging") {
			cleanupCalled = true
		}
	}
	if !cleanupCalled {
		t.Errorf("expected staging cleanup, commands: %v", conn.runLog)
	}

	// docker compose up must NOT have been called
	for _, command := range conn.runLog {
		if strings.Contains(command, " up") {
			t.Errorf("compose up should not be called after preflight failure, got: %q", command)
		}
	}
}

func TestDeploy_NoComposeFilesError(t *testing.T) {
	executor := newExec(t, &mockConn{}, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": ""})
	_, err := executor.Deploy(context.Background(), bundle, compose.DeployOptions{})
	if err == nil {
		t.Fatal("expected error for empty ComposeFiles")
	}
}

func TestDeploy_FilesUploadedToStaging(t *testing.T) {
	conn := &mockConn{
		responders: []responder{
			{match: "config", stdout: "services: {}\n"},
		},
	}

	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{
		"compose.yaml": "services: {}\n",
		".env":         "FOO=bar\n",
	})
	opts := compose.DeployOptions{ComposeFiles: []string{"compose.yaml"}}

	// Ignore the overall error since the mock mv commands return empty strings
	executor.Deploy(context.Background(), bundle, opts) //nolint:errcheck

	// Both files should have been uploaded to .staging/
	for _, file := range bundle.Files {
		found := false
		for _, upload := range conn.uploadLog {
			if strings.Contains(upload, ".staging/"+file.RemoteName) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q to be uploaded to .staging, uploads: %v", file.RemoteName, conn.uploadLog)
		}
	}
}

// PersistFiles

func TestDeploy_PersistFiles_CopiedFromLiveToStaging(t *testing.T) {
	psJSON := `{"Service":"web","State":"running"}`
	conn := &mockConn{
		responders: []responder{
			{match: "config", stdout: "services: {}\n"},
			{match: "ps", stdout: psJSON},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{
		ComposeFiles: []string{"compose.yaml"},
		PersistFiles: []string{".env"},
	}

	executor.Deploy(context.Background(), bundle, opts) //nolint:errcheck

	found := false
	for _, command := range conn.runLog {
		if strings.Contains(command, "if [ -f") && strings.Contains(command, ".env") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a persist-file copy command for .env, commands: %v", conn.runLog)
	}
}

func TestDeploy_PersistFiles_CopiedBeforePreflight(t *testing.T) {
	psJSON := `{"Service":"web","State":"running"}`
	conn := &mockConn{
		responders: []responder{
			{match: "config", stdout: "services: {}\n"},
			{match: "ps", stdout: psJSON},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{
		ComposeFiles: []string{"compose.yaml"},
		PersistFiles: []string{".env"},
	}

	executor.Deploy(context.Background(), bundle, opts) //nolint:errcheck

	copyIdx, preflightIdx := -1, -1
	for i, command := range conn.runLog {
		if strings.Contains(command, "if [ -f") && strings.Contains(command, ".env") {
			copyIdx = i
		}
		if strings.Contains(command, "config") {
			preflightIdx = i
		}
	}
	if copyIdx == -1 {
		t.Fatal("copy command not found")
	}
	if preflightIdx == -1 {
		t.Fatal("preflight command not found")
	}
	if copyIdx >= preflightIdx {
		t.Errorf("copy (idx %d) should run before preflight (idx %d)", copyIdx, preflightIdx)
	}
}

func TestDeploy_PersistFiles_MultiplePersistFiles(t *testing.T) {
	psJSON := `{"Service":"web","State":"running"}`
	conn := &mockConn{
		responders: []responder{
			{match: "config", stdout: "services: {}\n"},
			{match: "ps", stdout: psJSON},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{
		ComposeFiles: []string{"compose.yaml"},
		PersistFiles: []string{".env", "secrets.env"},
	}

	executor.Deploy(context.Background(), bundle, opts) //nolint:errcheck

	var copyCount int
	for _, command := range conn.runLog {
		if strings.Contains(command, "if [ -f") {
			copyCount++
		}
	}
	if copyCount != 2 {
		t.Errorf("expected 2 copy commands, got %d; commands: %v", copyCount, conn.runLog)
	}
}

func TestDeploy_NoPersistFiles_NoCopyCommand(t *testing.T) {
	psJSON := `{"Service":"web","State":"running"}`
	conn := &mockConn{
		responders: []responder{
			{match: "config", stdout: "services: {}\n"},
			{match: "ps", stdout: psJSON},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{ComposeFiles: []string{"compose.yaml"}}

	executor.Deploy(context.Background(), bundle, opts) //nolint:errcheck

	for _, command := range conn.runLog {
		if strings.Contains(command, "if [ -f") {
			t.Errorf("unexpected copy command when PersistFiles is empty: %q", command)
		}
	}
}

// The persist copy must be guarded so the repo bundle's file takes precedence:
// the host file is only restored when staging doesn't already have it.
func TestDeploy_PersistFiles_GuardedByBundlePrecedence(t *testing.T) {
	psJSON := `{"Service":"web","State":"running"}`
	conn := &mockConn{
		responders: []responder{
			{match: "config", stdout: "services: {}\n"},
			{match: "ps", stdout: psJSON},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{
		ComposeFiles: []string{"compose.yaml"},
		PersistFiles: []string{".env"},
	}

	executor.Deploy(context.Background(), bundle, opts) //nolint:errcheck

	var copyCommand string
	for _, command := range conn.runLog {
		if strings.Contains(command, "if [ -f") && strings.Contains(command, ".env") {
			copyCommand = command
			break
		}
	}
	if copyCommand == "" {
		t.Fatalf("persist copy command not found: %v", conn.runLog)
	}
	// The "! -e <staging path>" guard makes the copy a no-op when the bundle
	// already shipped the file, so the committed version is never overwritten.
	if !strings.Contains(copyCommand, "! -e") {
		t.Errorf("persist copy missing bundle-precedence guard (! -e): %q", copyCommand)
	}
	if !strings.Contains(copyCommand, ".staging/.env") {
		t.Errorf("persist copy guard should reference the staging path: %q", copyCommand)
	}
}

func TestDeploy_PersistFiles_CopyFailure_AbortsDeploy(t *testing.T) {
	conn := &mockConn{
		responders: []responder{
			{match: "if [ -f", err: fmt.Errorf("permission denied")},
		},
	}
	executor := newExec(t, conn, "/srv/apps/myapp")
	bundle := makeBundle(t, map[string]string{"compose.yaml": "services: {}\n"})
	opts := compose.DeployOptions{
		ComposeFiles: []string{"compose.yaml"},
		PersistFiles: []string{".env"},
	}

	_, err := executor.Deploy(context.Background(), bundle, opts)
	if err == nil {
		t.Fatal("expected error when persist file copy fails, got nil")
	}
	if !strings.Contains(err.Error(), "copy persist file") {
		t.Errorf("error should mention 'copy persist file': %v", err)
	}

	// Preflight and compose up must not have been called.
	for _, command := range conn.runLog {
		if strings.Contains(command, "compose") && strings.Contains(command, "config") {
			t.Errorf("preflight should not run after copy failure, got: %q", command)
		}
		if strings.Contains(command, " up") {
			t.Errorf("compose up should not run after copy failure, got: %q", command)
		}
	}
}

// Down

func TestDown(t *testing.T) {
	conn := &mockConn{}
	executor := newExec(t, conn, "/srv/apps/myapp")
	if err := executor.Down(context.Background(), []string{"compose.yaml"}); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if len(conn.runLog) != 1 || !strings.Contains(conn.runLog[0], "down") {
		t.Errorf("expected down command, got %v", conn.runLog)
	}
}

// Bundle

func TestBundleValidate(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		bundle := makeBundle(t, map[string]string{"compose.yaml": ""})
		if err := bundle.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
	t.Run("missing local file", func(t *testing.T) {
		bundle := &compose.Bundle{Files: []compose.BundleFile{
			{LocalPath: "/nonexistent/file.yaml", RemoteName: "compose.yaml"},
		}}
		if err := bundle.Validate(); err == nil {
			t.Fatal("expected error for missing local file, got nil")
		}
	})
	t.Run("empty RemoteName", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.yaml")
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatal(err)
		}
		bundle := &compose.Bundle{Files: []compose.BundleFile{
			{LocalPath: path, RemoteName: ""},
		}}
		if err := bundle.Validate(); err == nil {
			t.Fatal("expected error for empty RemoteName, got nil")
		}
	})
}
