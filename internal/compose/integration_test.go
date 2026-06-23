//go:build integration

package compose_test

// Integration test: deploys a real minimal Compose app through an in-process
// SSH server to the local Docker Engine.
//
// Requirements:
//   - Docker installed and running locally
//   - `docker compose` (v2 plugin) available
//
// Run with:
//
//	make integration-test
//	go test -v -race -tags integration -timeout 5m ./internal/compose/...

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/compose"
	"github.com/jancernik/deeplo/internal/ssh"
	"github.com/jancernik/deeplo/internal/testutils"
)

// minimalComposeYAML is a tiny Compose project that runs busybox indefinitely.
// We use busybox:latest (always available, tiny) with sleep so `ps` reports it running.
const minimalComposeYAML = `services:
  svc:
    image: busybox:latest
    command: ["sleep", "300"]
`

// dialLocalSSH starts an in-process SSH test server (reused from the ssh package)
// and returns an open ssh.Connection running exec commands on localhost.
func dialLocalSSH(t *testing.T) ssh.Connection {
	t.Helper()

	server, keys := testutils.StartSSHServer(t)

	dialer := ssh.NewDialer()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dialer.Dial(ctx, ssh.DialConfig{
		Address:        "127.0.0.1",
		Port:           server.Port(),
		User:           "testuser",
		PrivateKeyFile: keys.PrivateKeyFile,
		KnownHostsFile: keys.KnownHostsFile,
	})
	if err != nil {
		t.Fatalf("dial local SSH: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestDeployIntegration(t *testing.T) {
	// Verify docker compose is available before starting.
	conn := dialLocalSSH(t)
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer checkCancel()
	if _, _, err := conn.Run(checkCtx, "docker compose version"); err != nil {
		t.Skipf("docker compose not available on this host: %v", err)
	}

	// Use a temp dir as the "remote" project directory.
	// Since the SSH server runs commands locally, the temp dir IS both local and remote.
	projectDir := t.TempDir()

	executor := compose.NewExecutor(conn, projectDir, "itest", slog.Default())

	// Write the compose file locally (source for the bundle).
	localDir := t.TempDir()
	composeFile := filepath.Join(localDir, "compose.yaml")
	if err := os.WriteFile(composeFile, []byte(minimalComposeYAML), 0644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	bundle := &compose.Bundle{
		Files: []compose.BundleFile{
			{LocalPath: composeFile, RemoteName: "compose.yaml"},
		},
	}
	options := compose.DeployOptions{
		ComposeFiles: []string{"compose.yaml"},
	}

	// Register cleanup BEFORE calling Deploy so containers are torn down even
	// if the test assertions below fail.
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := executor.Down(cleanCtx, options.ComposeFiles); err != nil {
			t.Logf("compose down (cleanup): %v", err)
		}
	})

	// Deploy
	deployCtx, deployCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer deployCancel()

	result, err := executor.Deploy(deployCtx, bundle, options)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// Verify containers are running
	psCtx, psCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer psCancel()

	services, err := executor.Ps(psCtx, options.ComposeFiles)
	if err != nil {
		t.Fatalf("Ps after deploy: %v", err)
	}
	if len(services) == 0 && len(result.Services) == 0 {
		t.Error("expected at least one service running after deploy, got none")
	}

	// Find our service.
	var found *compose.ServiceStatus
	all := append(services, result.Services...)
	for index, service := range all {
		if service.Service == "svc" || strings.HasSuffix(service.Name, "-svc-1") {
			found = &all[index]
			break
		}
	}
	if found == nil {
		t.Errorf("service 'svc' not found in ps output: %+v", services)
	} else {
		state := strings.ToLower(found.State)
		if state != "running" {
			t.Errorf("service state: got %q, want running", found.State)
		}
		t.Logf("service status: name=%s state=%s status=%s", found.Name, found.State, found.Status)
	}

}
