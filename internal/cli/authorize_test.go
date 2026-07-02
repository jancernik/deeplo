package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jancernik/deeplo/internal/api"
)

// authorizeCall records one captured key-install invocation.
type authorizeCall struct {
	publicKey string
	port      int
	target    string
}

// setupAuthorize writes a config and deploy key pair into a temp dir, points the
// bootstrap env at them, and stubs the SSH lookup and key install, returning the
// captured-calls slice.
func setupAuthorize(t *testing.T, configYAML string) *[]authorizeCall {
	t.Helper()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(configFile, []byte(configYAML), 0600); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "deploy_key")
	if err := os.WriteFile(keyFile+".pub", []byte("ssh-ed25519 AAAAtest deeplo\n"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DEEPLO_CONFIG_FILE", configFile)
	t.Setenv("DEEPLO_SSH_PRIVATE_KEY_FILE", keyFile)
	t.Setenv("DEEPLO_DATA_DIR", dir)

	origLookup := lookupSSH
	t.Cleanup(func() { lookupSSH = origLookup })
	lookupSSH = func() error { return nil }

	origInstall := installAuthorizedKey
	t.Cleanup(func() { installAuthorizedKey = origInstall })
	var calls []authorizeCall
	installAuthorizedKey = func(publicKey []byte, port int, target string) error {
		calls = append(calls, authorizeCall{string(publicKey), port, target})
		return nil
	}

	// Default to a daemon that reaches nothing, so every host is authorized.
	origProbe := probeAuthorizedHosts
	t.Cleanup(func() { probeAuthorizedHosts = origProbe })
	probeAuthorizedHosts = func(*cobra.Command) map[string]bool { return nil }
	return &calls
}

func runAuthorizeCmd(t *testing.T, args []string) (string, error) {
	t.Helper()
	var out strings.Builder
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(&out)
	root.AddCommand(AuthorizeCmd())
	root.SetArgs(append([]string{"authorize"}, args...))
	err := root.Execute()
	return out.String(), err
}

const twoHostConfig = `
hosts:
  - name: web-1
    address: 10.0.0.10
    deploy_dir: /srv/apps
  - name: web-2
    address: 10.0.0.20
    deploy_dir: /srv/apps
    user: ops
    port: 2222
`

// TestAuthorizeAllHosts verifies that with no args every configured host is
// authorized, resolving user/port/address from config and bootstrap defaults.
func TestAuthorizeAllHosts(t *testing.T) {
	calls := setupAuthorize(t, twoHostConfig)

	out, err := runAuthorizeCmd(t, nil)
	if err != nil {
		t.Fatalf("authorize: unexpected error: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected 2 installs, got %d: %+v", len(*calls), *calls)
	}

	// web-1 uses the default SSH user (deploy) and default port (22 -> omitted).
	if got := (*calls)[0]; got.target != "deploy@10.0.0.10" || got.port != 22 {
		t.Errorf("web-1: got target=%q port=%d, want deploy@10.0.0.10 / 22", got.target, got.port)
	}
	// web-2 overrides both user and port.
	if got := (*calls)[1]; got.target != "ops@10.0.0.20" || got.port != 2222 {
		t.Errorf("web-2: got target=%q port=%d, want ops@10.0.0.20 / 2222", got.target, got.port)
	}
	// The deploy public key is passed through to each host.
	if got := (*calls)[0].publicKey; !strings.Contains(got, "ssh-ed25519 AAAAtest deeplo") {
		t.Errorf("expected the deploy public key, got %q", got)
	}
	if !strings.Contains(out, "Authorized web-1") || !strings.Contains(out, "Authorized web-2") {
		t.Errorf("expected success output for both hosts, got: %q", out)
	}
}

// TestAuthorizeSkipsAlreadyAuthorized verifies that hosts the daemon can already
// reach are skipped without an install, while the rest are still authorized.
func TestAuthorizeSkipsAlreadyAuthorized(t *testing.T) {
	calls := setupAuthorize(t, twoHostConfig)
	probeAuthorizedHosts = func(*cobra.Command) map[string]bool {
		return map[string]bool{"web-1": true}
	}

	out, err := runAuthorizeCmd(t, nil)
	if err != nil {
		t.Fatalf("authorize: unexpected error: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 install (web-2 only), got %d: %+v", len(*calls), *calls)
	}
	if got := (*calls)[0].target; got != "ops@10.0.0.20" {
		t.Errorf("expected only web-2 to be authorized, got target=%q", got)
	}
	if !strings.Contains(out, "web-1") || !strings.Contains(out, "already authorized") {
		t.Errorf("expected web-1 to be reported as already authorized, got: %q", out)
	}
}

// TestAuthorizeSpecificHost verifies that naming a host authorizes only it.
func TestAuthorizeSpecificHost(t *testing.T) {
	calls := setupAuthorize(t, twoHostConfig)

	if _, err := runAuthorizeCmd(t, []string{"web-2"}); err != nil {
		t.Fatalf("authorize web-2: unexpected error: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 install, got %d", len(*calls))
	}
	if got := (*calls)[0]; got.target != "ops@10.0.0.20" {
		t.Errorf("got target=%q, want ops@10.0.0.20", got.target)
	}
}

// TestAuthorizeUnknownHost verifies that an unknown host name is rejected
// before any install runs.
func TestAuthorizeUnknownHost(t *testing.T) {
	calls := setupAuthorize(t, twoHostConfig)

	_, err := runAuthorizeCmd(t, []string{"nope"})
	if err == nil {
		t.Fatal("expected an error for an unknown host, got nil")
	}
	if len(*calls) != 0 {
		t.Errorf("expected no installs, got %d", len(*calls))
	}
}

// TestAuthorizeNoHosts verifies a clear error when the config has no hosts.
func TestAuthorizeNoHosts(t *testing.T) {
	calls := setupAuthorize(t, "hosts: []\n")

	_, err := runAuthorizeCmd(t, nil)
	if err == nil {
		t.Fatal("expected an error when no hosts are configured, got nil")
	}
	if len(*calls) != 0 {
		t.Errorf("expected no installs, got %d", len(*calls))
	}
}

// TestAuthorizeMissingSSH verifies the command fails when ssh is not installed,
// without attempting any host.
func TestAuthorizeMissingSSH(t *testing.T) {
	calls := setupAuthorize(t, twoHostConfig)
	lookupSSH = func() error { return errNotNative } // any non-nil error

	_, err := runAuthorizeCmd(t, nil)
	if err == nil {
		t.Fatal("expected an error when ssh is missing, got nil")
	}
	if len(*calls) != 0 {
		t.Errorf("expected no installs, got %d", len(*calls))
	}
}

// TestProbeAuthorizedHostsFiltersReachable exercises the real precheck against a
// fake daemon: only hosts the daemon reports OK end up in the returned set.
func TestProbeAuthorizedHostsFiltersReachable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/probe", serveJSON(api.ProbeResponse{Hosts: []api.ProbeHost{
		{Host: "web-1", OK: true},
		{Host: "web-2", Error: "dial timeout"},
	}}))
	withAdminSocket(t, startFakeDaemon(t, mux))

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	got := probeAuthorizedHosts(cmd)
	if !got["web-1"] || got["web-2"] {
		t.Fatalf("expected only web-1 authorized, got %+v", got)
	}
}

// TestProbeAuthorizedHostsDaemonDown verifies the precheck degrades to an empty
// set (not an error) when the daemon is unreachable, so authorize falls back to
// installing on every host.
func TestProbeAuthorizedHostsDaemonDown(t *testing.T) {
	withAdminSocket(t, "/nonexistent.sock")

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if got := probeAuthorizedHosts(cmd); got != nil {
		t.Fatalf("expected nil when daemon unreachable, got %+v", got)
	}
}
