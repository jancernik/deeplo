package ssh_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/ssh"
)

func dialTestServer(t *testing.T) ssh.Connection {
	t.Helper()
	srv, keys := startTestSSHServer(t)

	dialer := ssh.NewDialer()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dialer.Dial(ctx, ssh.DialConfig{
		Address:        "127.0.0.1",
		Port:           srv.port(),
		User:           "testuser",
		PrivateKeyFile: keys.privateKeyFile,
		KnownHostsFile: keys.knownHostsFile,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// Dial

func TestDial(t *testing.T) {
	conn := dialTestServer(t)
	if conn == nil {
		t.Fatal("Dial returned nil connection")
	}
}

func TestDialHostKeyRejection(t *testing.T) {
	srv, keys := startTestSSHServer(t)
	wrongKnownHosts := srv.wrongKnownHostsFile(t)

	dialer := ssh.NewDialer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := dialer.Dial(ctx, ssh.DialConfig{
		Address:        "127.0.0.1",
		Port:           srv.port(),
		User:           "testuser",
		PrivateKeyFile: keys.privateKeyFile,
		KnownHostsFile: wrongKnownHosts,
	})
	if err == nil {
		t.Fatal("expected error for wrong host key, got nil")
	}
}

func TestDialMissingPrivateKey(t *testing.T) {
	srv, keys := startTestSSHServer(t)
	dialer := ssh.NewDialer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := dialer.Dial(ctx, ssh.DialConfig{
		Address:        "127.0.0.1",
		Port:           srv.port(),
		User:           "testuser",
		PrivateKeyFile: "/nonexistent/key",
		KnownHostsFile: keys.knownHostsFile,
	})
	if err == nil {
		t.Fatal("expected error for missing private key, got nil")
	}
}

// Run

func TestRun_Success(t *testing.T) {
	conn := dialTestServer(t)

	stdout, stderr, err := conn.Run(context.Background(), "echo hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(stdout) != "hello" {
		t.Errorf("stdout: got %q, want %q", stdout, "hello")
	}
	if stderr != "" {
		t.Errorf("stderr: got %q, want empty", stderr)
	}
}

func TestRun_MultilineOutput(t *testing.T) {
	conn := dialTestServer(t)

	stdout, _, err := conn.Run(context.Background(), "printf 'line1\nline2\nline3\n'")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Errorf("got %d lines, want 3: %q", len(lines), stdout)
	}
}

func TestRun_NonzeroExit(t *testing.T) {
	conn := dialTestServer(t)

	_, _, err := conn.Run(context.Background(), "exit 42")
	if err == nil {
		t.Fatal("expected error for exit 42, got nil")
	}
}

func TestRun_Stderr(t *testing.T) {
	conn := dialTestServer(t)

	_, stderr, _ := conn.Run(context.Background(), "echo errout >&2")
	if !strings.Contains(stderr, "errout") {
		t.Errorf("stderr: got %q, want to contain 'errout'", stderr)
	}
}

func TestRun_ContextCancel(t *testing.T) {
	conn := dialTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := conn.Run(ctx, "sleep 10")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("cancellation took too long: %v", elapsed)
	}
}

// Upload

func TestUpload(t *testing.T) {
	conn := dialTestServer(t)

	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "src.txt")
	if err := os.WriteFile(localFile, []byte("hello upload"), 0644); err != nil {
		t.Fatalf("write local: %v", err)
	}

	remoteDir := t.TempDir()
	remotePath := filepath.Join(remoteDir, "sub", "dst.txt")

	if err := conn.Upload(context.Background(), localFile, remotePath); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	got, err := os.ReadFile(remotePath)
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(got) != "hello upload" {
		t.Errorf("content: got %q, want %q", got, "hello upload")
	}
}

// Probe

func TestProbe(t *testing.T) {
	conn := dialTestServer(t)

	if err := ssh.Probe(context.Background(), conn); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

// multiple commands on the same connection

func TestMultipleRuns(t *testing.T) {
	conn := dialTestServer(t)

	for i := range 5 {
		stdout, _, err := conn.Run(context.Background(), "echo ping")
		if err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		if strings.TrimSpace(stdout) != "ping" {
			t.Errorf("Run %d: got %q, want ping", i, stdout)
		}
	}
}

// upload then verify via Run

func TestUploadThenRun(t *testing.T) {
	conn := dialTestServer(t)

	dir := t.TempDir()
	localFile := filepath.Join(dir, "data.txt")
	content := "verified content"
	if err := os.WriteFile(localFile, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	remotePath := filepath.Join(t.TempDir(), "data.txt")
	if err := conn.Upload(context.Background(), localFile, remotePath); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	stdout, _, err := conn.Run(context.Background(), "cat "+remotePath)
	if err != nil {
		t.Fatalf("Run cat: %v", err)
	}
	if stdout != content {
		t.Errorf("got %q, want %q", stdout, content)
	}
}
