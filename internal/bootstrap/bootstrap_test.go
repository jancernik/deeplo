package bootstrap_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
)

// helpers

// setEnv sets env vars from a map and restores them in t.Cleanup.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		old, hadOld := os.LookupEnv(k)
		t.Cleanup(func() {
			if hadOld {
				_ = os.Setenv(k, old)
			} else {
				_ = os.Unsetenv(k)
			}
		})
		_ = os.Setenv(k, v)
	}
}

// clearBootstrapEnv unsets all bootstrap env vars so tests start clean.
func clearBootstrapEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DEEPLO_CONFIG_FILE",
		"DEEPLO_CONFIG_REPO_URL", "DEEPLO_CONFIG_REPO_BRANCH", "DEEPLO_CONFIG_REPO_FILE",
		"DEEPLO_CONFIG_REPO_MODE", "DEEPLO_CONFIG_REPO_INTERVAL",
		"DEEPLO_CONFIG_WATCH",
		"DEEPLO_DATA_DIR", "DEEPLO_SSH_KNOWN_HOSTS", "DEEPLO_SSH_HOST_KEY_POLICY",
		"DEEPLO_SSH_PRIVATE_KEY_FILE", "DEEPLO_SSH_USER", "DEEPLO_SSH_PORT",
		"DEEPLO_HTTP_PORT", "DEEPLO_PUBLIC_URL", "DEEPLO_UNIX_SOCKET",
		"DEEPLO_LOG_SERVER", "DEEPLO_LOG_SERVER_PORT",
		"DEEPLO_MAX_WORKERS", "DEEPLO_HOST_PARALLEL",
		"DEEPLO_DEPLOY_TIMEOUT", "DEEPLO_SHUTDOWN_GRACE",
		"DEEPLO_LOG_RETENTION_DAYS",
		"DEEPLO_LOG_LEVEL", "DEEPLO_LOG_FORMAT",
		"DEEPLO_GITHUB_WEBHOOK_SECRET_FILE", "DEEPLO_GITHUB_TOKEN_FILE",
		"DEEPLO_GITHUB_ENVIRONMENT", "DEEPLO_GITHUB_ENVIRONMENT_HOST",
	} {
		old, hadOld := os.LookupEnv(k)
		t.Cleanup(func() {
			if hadOld {
				_ = os.Setenv(k, old)
			} else {
				_ = os.Unsetenv(k)
			}
		})
		_ = os.Unsetenv(k)
	}
}

// requireGit skips the test if git is not on PATH.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH; skipping")
	}
}

// minimalConfigYAML is a valid managed config; runtime settings live in
// bootstrap env vars, not in the YAML.
const minimalConfigYAML = `
version: 1
hosts:
  - name: h1
    address: 10.0.0.1
    deploy_dir: /srv/apps
repos:
  - name: r1
    url: git@github.com:org/repo.git
    branch: main
    trigger_mode: webhook
projects:
  - name: p1
    repo: r1
    repo_subdir: apps/p1
    targets:
      - h1
`

// setupConfigRepo creates a local bare git repo containing a valid config file.
// Returns the bare repo path (usable as a clone URL) and the commit SHA.
func setupConfigRepo(t *testing.T, configContent string) (bareDir, sha string) {
	t.Helper()
	requireGit(t)

	base := t.TempDir()
	bare := filepath.Join(base, "config.git")
	work := filepath.Join(base, "work")

	mustGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustGit("", "init", "--bare", "--initial-branch=main", bare)
	mustGit("", "init", "--initial-branch=main", work)
	mustGit(work, "config", "user.email", "test@example.com")
	mustGit(work, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(work, "config.yml"), []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(work, "add", ".")
	mustGit(work, "commit", "-m", "initial")
	mustGit(work, "remote", "add", "origin", bare)
	mustGit(work, "push", "origin", "main")

	out, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return bare, string(out[:40])
}

// writeLocalConfig writes a YAML config to a temp file and returns its path.
func writeLocalConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Load (env parsing)

func TestLoad_Defaults(t *testing.T) {
	clearBootstrapEnv(t)

	bc := bootstrap.LoadEnv()

	if bc.Source != bootstrap.SourceLocal {
		t.Errorf("Source: got %q, want %q", bc.Source, bootstrap.SourceLocal)
	}
	if bc.ConfigRepoBranch != "main" {
		t.Errorf("ConfigRepoBranch: got %q, want main", bc.ConfigRepoBranch)
	}
	if bc.ConfigRepoFile != "config.yml" {
		t.Errorf("ConfigRepoFile: got %q, want config.yml", bc.ConfigRepoFile)
	}
	if bc.SSHUser != "deploy" {
		t.Errorf("SSHUser: got %q, want deploy", bc.SSHUser)
	}
	if bc.SSHPort != 22 {
		t.Errorf("SSHPort: got %d, want 22", bc.SSHPort)
	}
	if bc.SSHHostKeyPolicy != "accept-new" {
		t.Errorf("SSHHostKeyPolicy: got %q, want accept-new", bc.SSHHostKeyPolicy)
	}
	if bc.HTTPPort != 8470 {
		t.Errorf("HTTPPort: got %d, want 8470", bc.HTTPPort)
	}
	if bc.LogServer || bc.LogServerEnabled() {
		t.Errorf("log server should be disabled by default, got LogServer=%v enabled=%v", bc.LogServer, bc.LogServerEnabled())
	}
	if bc.LogServerPort != 8470 {
		t.Errorf("LogServerPort: got %d, want 8470", bc.LogServerPort)
	}
	if bc.LogRetentionDays != 14 {
		t.Errorf("LogRetentionDays: got %d, want 14", bc.LogRetentionDays)
	}
	if bc.GitHubEnvironment != "" {
		t.Errorf("GitHubEnvironment: got %q, want empty (no default)", bc.GitHubEnvironment)
	}
	if bc.DataPath != "" {
		t.Errorf("DataPath should be empty by default, got %q", bc.DataPath)
	}
	if bc.ConfigRepoMode != "poll" {
		t.Errorf("ConfigRepoMode: got %q, want poll", bc.ConfigRepoMode)
	}
	if bc.ConfigRepoInterval != 60*time.Second {
		t.Errorf("ConfigRepoInterval: got %v, want 60s", bc.ConfigRepoInterval)
	}
	if bc.ShutdownGrace != 30*time.Second {
		t.Errorf("ShutdownGrace: got %v, want 30s", bc.ShutdownGrace)
	}
	if bc.LogLevel != "info" {
		t.Errorf("LogLevel: got %q, want info", bc.LogLevel)
	}
	if bc.LogFormat != "pretty" {
		t.Errorf("LogFormat: got %q, want pretty", bc.LogFormat)
	}
	if bc.ConfigWatch {
		t.Errorf("ConfigWatch: got true, want false (disabled by default)")
	}
}

func TestLoad_LogServer(t *testing.T) {
	cases := []struct {
		name           string
		env            map[string]string
		wantEnabled    bool
		wantListenPort int
	}{
		{"disabled by default", nil, false, 8470},
		{"toggle on uses the http port", map[string]string{"DEEPLO_LOG_SERVER": "true"}, true, 8470},
		{"port alone does not enable", map[string]string{"DEEPLO_LOG_SERVER_PORT": "9100"}, false, 9100},
		{"toggle and dedicated port", map[string]string{"DEEPLO_LOG_SERVER": "true", "DEEPLO_LOG_SERVER_PORT": "9100"}, true, 9100},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			clearBootstrapEnv(t)
			setEnv(t, testCase.env)

			bc := bootstrap.LoadEnv()
			if bc.LogServerEnabled() != testCase.wantEnabled {
				t.Errorf("LogServerEnabled() = %v, want %v", bc.LogServerEnabled(), testCase.wantEnabled)
			}
			if bc.LogServerListenPort() != testCase.wantListenPort {
				t.Errorf("LogServerListenPort() = %d, want %d", bc.LogServerListenPort(), testCase.wantListenPort)
			}
		})
	}
}

func TestValidate_LogServerPort(t *testing.T) {
	base := func() *bootstrap.Config {
		return &bootstrap.Config{
			Source:            bootstrap.SourceLocal,
			DataPath:          "/var/lib/deeplo",
			SSHPrivateKeyFile: "/run/secrets/deploy_key",
			SSHPort:           22,
			SSHHostKeyPolicy:  "accept-new",
		}
	}
	hasPortError := func(errs []bootstrap.ValidationError) bool {
		for _, e := range errs {
			if e.Field == "DEEPLO_LOG_SERVER_PORT" {
				return true
			}
		}
		return false
	}

	bc := base()
	bc.LogServerPort = 9100
	if hasPortError(bc.Validate()) {
		t.Errorf("port 9100 should be valid, got: %v", bc.Validate())
	}

	bc = base()
	bc.LogServerPort = 70000
	if !hasPortError(bc.Validate()) {
		t.Errorf("port 70000 should be rejected, got: %v", bc.Validate())
	}
}

func TestLoad_LoggingAndWatch(t *testing.T) {
	cases := []struct {
		name            string
		env             map[string]string
		wantLogLevel    string
		wantLogFormat   string
		wantConfigWatch bool
	}{
		{
			name:            "defaults: info/pretty, watch disabled",
			env:             map[string]string{},
			wantLogLevel:    "info",
			wantLogFormat:   "pretty",
			wantConfigWatch: false,
		},
		{
			name:            "debug level and json format",
			env:             map[string]string{"DEEPLO_LOG_LEVEL": "debug", "DEEPLO_LOG_FORMAT": "json"},
			wantLogLevel:    "debug",
			wantLogFormat:   "json",
			wantConfigWatch: false,
		},
		{
			name:            "config watch enabled",
			env:             map[string]string{"DEEPLO_CONFIG_WATCH": "true"},
			wantLogLevel:    "info",
			wantLogFormat:   "pretty",
			wantConfigWatch: true,
		},
		{
			name:            "config watch disabled when set to non-boolean",
			env:             map[string]string{"DEEPLO_CONFIG_WATCH": "yes"},
			wantLogLevel:    "info",
			wantLogFormat:   "pretty",
			wantConfigWatch: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			clearBootstrapEnv(t)
			setEnv(t, testCase.env)

			bc := bootstrap.LoadEnv()
			if bc.LogLevel != testCase.wantLogLevel {
				t.Errorf("LogLevel: got %q, want %q", bc.LogLevel, testCase.wantLogLevel)
			}
			if bc.LogFormat != testCase.wantLogFormat {
				t.Errorf("LogFormat: got %q, want %q", bc.LogFormat, testCase.wantLogFormat)
			}
			if bc.ConfigWatch != testCase.wantConfigWatch {
				t.Errorf("ConfigWatch: got %v, want %v", bc.ConfigWatch, testCase.wantConfigWatch)
			}
		})
	}
}

func TestLoad_LocalMode(t *testing.T) {
	clearBootstrapEnv(t)
	setEnv(t, map[string]string{
		"DEEPLO_CONFIG_FILE": "/etc/deeplo/config.yml",
		"DEEPLO_DATA_DIR":    "/var/lib/deeplo",
		"DEEPLO_HTTP_PORT":   "9090",
	})

	bc := bootstrap.LoadEnv()

	if bc.Source != bootstrap.SourceLocal {
		t.Errorf("Source: got %q, want local", bc.Source)
	}
	if bc.ConfigFile != "/etc/deeplo/config.yml" {
		t.Errorf("ConfigFile: got %q", bc.ConfigFile)
	}
	if bc.DataPath != "/var/lib/deeplo" {
		t.Errorf("DataPath: got %q", bc.DataPath)
	}
	if bc.HTTPPort != 9090 {
		t.Errorf("HTTPPort: got %d", bc.HTTPPort)
	}
}

func TestLoad_GitMode(t *testing.T) {
	clearBootstrapEnv(t)
	setEnv(t, map[string]string{
		"DEEPLO_CONFIG_REPO_URL":    "git@github.com:me/config.git",
		"DEEPLO_CONFIG_REPO_BRANCH": "prod",
		"DEEPLO_CONFIG_REPO_FILE":   "deeplo.yml",
		"DEEPLO_SSH_KNOWN_HOSTS":    "/etc/deeplo/known_hosts",
		"DEEPLO_DATA_DIR":           "/var/lib/deeplo",
		"DEEPLO_PUBLIC_URL":         "https://deeplo.example.com",
	})

	bc := bootstrap.LoadEnv()

	if bc.Source != bootstrap.SourceGit {
		t.Errorf("Source: got %q, want git", bc.Source)
	}
	if bc.ConfigRepoURL != "git@github.com:me/config.git" {
		t.Errorf("ConfigRepoURL: got %q", bc.ConfigRepoURL)
	}
	if bc.ConfigRepoBranch != "prod" {
		t.Errorf("ConfigRepoBranch: got %q", bc.ConfigRepoBranch)
	}
	if bc.ConfigRepoFile != "deeplo.yml" {
		t.Errorf("ConfigRepoFile: got %q", bc.ConfigRepoFile)
	}
	if bc.SSHKnownHosts != "/etc/deeplo/known_hosts" {
		t.Errorf("SSHKnownHosts: got %q", bc.SSHKnownHosts)
	}
	if bc.PublicURL != "https://deeplo.example.com" {
		t.Errorf("PublicURL: got %q", bc.PublicURL)
	}
}

func TestLoad_ConfigRepoTrigger(t *testing.T) {
	cases := []struct {
		name         string
		env          map[string]string
		wantMode     string
		wantInterval time.Duration
	}{
		{
			name:         "defaults",
			env:          map[string]string{},
			wantMode:     "poll",
			wantInterval: 60 * time.Second,
		},
		{
			name:         "webhook mode",
			env:          map[string]string{"DEEPLO_CONFIG_REPO_MODE": "webhook"},
			wantMode:     "webhook",
			wantInterval: 60 * time.Second,
		},
		{
			name:         "hybrid mode with custom interval",
			env:          map[string]string{"DEEPLO_CONFIG_REPO_MODE": "hybrid", "DEEPLO_CONFIG_REPO_INTERVAL": "30s"},
			wantMode:     "hybrid",
			wantInterval: 30 * time.Second,
		},
		{
			name:         "poll mode with custom interval",
			env:          map[string]string{"DEEPLO_CONFIG_REPO_MODE": "poll", "DEEPLO_CONFIG_REPO_INTERVAL": "5m"},
			wantMode:     "poll",
			wantInterval: 5 * time.Minute,
		},
		{
			name:         "invalid duration falls back to default",
			env:          map[string]string{"DEEPLO_CONFIG_REPO_INTERVAL": "notaduration"},
			wantMode:     "poll",
			wantInterval: 60 * time.Second,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			clearBootstrapEnv(t)
			setEnv(t, testCase.env)

			bc := bootstrap.LoadEnv()
			if bc.ConfigRepoMode != testCase.wantMode {
				t.Errorf("ConfigRepoMode: got %q, want %q", bc.ConfigRepoMode, testCase.wantMode)
			}
			if bc.ConfigRepoInterval != testCase.wantInterval {
				t.Errorf("ConfigRepoInterval: got %v, want %v", bc.ConfigRepoInterval, testCase.wantInterval)
			}
		})
	}
}

// Validate

func TestValidate_LocalMode(t *testing.T) {
	bc := &bootstrap.Config{
		Source:            bootstrap.SourceLocal,
		DataPath:          "/var/lib/deeplo",
		SSHPrivateKeyFile: "/run/secrets/deploy_key",
		SSHPort:           22,
		SSHHostKeyPolicy:  "accept-new",
	}
	if errs := bc.Validate(); len(errs) != 0 {
		t.Errorf("valid local mode config should have no errors; got: %v", errs)
	}
}

func TestValidate_GitMode_Valid(t *testing.T) {
	bc := &bootstrap.Config{
		Source:            bootstrap.SourceGit,
		ConfigRepoURL:     "git@github.com:me/config.git",
		ConfigRepoBranch:  "main",
		ConfigRepoFile:    "config.yml",
		SSHKnownHosts:     "/etc/deeplo/known_hosts",
		DataPath:          "/var/lib/deeplo",
		SSHPrivateKeyFile: "/run/secrets/deploy_key",
		SSHPort:           22,
		SSHHostKeyPolicy:  "accept-new",
	}
	if errs := bc.Validate(); len(errs) != 0 {
		t.Errorf("expected no errors for valid git mode; got: %v", errs)
	}
}

func TestValidate_GitMode_MissingFields(t *testing.T) {
	// base is a fully-valid git-mode config; each case removes one field.
	base := bootstrap.Config{
		Source:            bootstrap.SourceGit,
		ConfigRepoURL:     "git@github.com:me/config.git",
		DataPath:          "/var/lib/deeplo",
		SSHPrivateKeyFile: "/run/secrets/deploy_key",
		SSHPort:           22,
		SSHHostKeyPolicy:  "accept-new",
	}
	cases := []struct {
		name        string
		bc          bootstrap.Config
		wantEnvVars []string
	}{
		{
			name:        "missing URL",
			bc:          func() bootstrap.Config { c := base; c.ConfigRepoURL = ""; return c }(),
			wantEnvVars: []string{"DEEPLO_CONFIG_REPO_URL"},
		},
		{
			name:        "missing data_dir",
			bc:          func() bootstrap.Config { c := base; c.DataPath = ""; return c }(),
			wantEnvVars: []string{"DEEPLO_DATA_DIR"},
		},
		{
			name:        "missing ssh key",
			bc:          func() bootstrap.Config { c := base; c.SSHPrivateKeyFile = ""; return c }(),
			wantEnvVars: []string{"DEEPLO_SSH_PRIVATE_KEY_FILE"},
		},
		{
			name: "multiple missing",
			bc: bootstrap.Config{
				Source:           bootstrap.SourceGit,
				SSHPort:          22,
				SSHHostKeyPolicy: "accept-new",
			},
			wantEnvVars: []string{"DEEPLO_DATA_DIR", "DEEPLO_SSH_PRIVATE_KEY_FILE", "DEEPLO_CONFIG_REPO_URL"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			errs := testCase.bc.Validate()
			if len(errs) != len(testCase.wantEnvVars) {
				t.Fatalf("got %d errors, want %d: %v", len(errs), len(testCase.wantEnvVars), errs)
			}
			for i, want := range testCase.wantEnvVars {
				if errs[i].Field != want {
					t.Errorf("error[%d].Field: got %q, want %q", i, errs[i].Field, want)
				}
			}
		})
	}
}

func TestValidate_ConfigRepoMode(t *testing.T) {
	bc := &bootstrap.Config{
		Source:            bootstrap.SourceGit,
		ConfigRepoURL:     "git@github.com:me/config.git",
		DataPath:          "/var/lib/deeplo",
		SSHPrivateKeyFile: "/run/secrets/deploy_key",
		SSHPort:           22,
		SSHHostKeyPolicy:  "accept-new",
		ConfigRepoMode:    "ftp", // invalid
	}
	errs := bc.Validate()
	found := false
	for _, e := range errs {
		if e.Field == "DEEPLO_CONFIG_REPO_MODE" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected DEEPLO_CONFIG_REPO_MODE error for %q; got: %v", bc.ConfigRepoMode, errs)
	}
}

func TestValidate_GitMode_TriggerModes(t *testing.T) {
	base := bootstrap.Config{
		Source:            bootstrap.SourceGit,
		ConfigRepoURL:     "git@github.com:me/config.git",
		DataPath:          "/var/lib/deeplo",
		SSHPrivateKeyFile: "/run/secrets/deploy_key",
		SSHPort:           22,
		SSHHostKeyPolicy:  "accept-new",
	}

	// Poll-based modes are valid without a webhook secret.
	for _, mode := range []string{"", "poll"} {
		t.Run("mode="+mode+"_no_secret", func(t *testing.T) {
			bc := base
			bc.ConfigRepoMode = mode
			if errs := bc.Validate(); len(errs) != 0 {
				t.Errorf("trigger %q should be valid without secret; got: %v", mode, errs)
			}
		})
	}

	// Webhook-based modes require DEEPLO_GITHUB_WEBHOOK_SECRET_FILE.
	for _, mode := range []string{"webhook", "hybrid"} {
		t.Run("mode="+mode+"_missing_secret", func(t *testing.T) {
			bc := base
			bc.ConfigRepoMode = mode
			errs := bc.Validate()
			found := false
			for _, e := range errs {
				if e.Field == "DEEPLO_GITHUB_WEBHOOK_SECRET_FILE" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected DEEPLO_GITHUB_WEBHOOK_SECRET_FILE error for mode %q without secret; got: %v", mode, errs)
			}
		})
		t.Run("mode="+mode+"_with_secret", func(t *testing.T) {
			bc := base
			bc.ConfigRepoMode = mode
			bc.GitHubWebhookSecretFile = "/run/secrets/webhook_secret"
			if errs := bc.Validate(); len(errs) != 0 {
				t.Errorf("trigger %q should be valid with secret configured; got: %v", mode, errs)
			}
		})
	}
}

func TestUnixSocketPath(t *testing.T) {
	t.Run("falls back to the default when unset", func(t *testing.T) {
		t.Setenv("DEEPLO_UNIX_SOCKET", "")
		if got := bootstrap.UnixSocketPath(); got != bootstrap.DefaultUnixSocket {
			t.Errorf("UnixSocketPath() = %q, want %q", got, bootstrap.DefaultUnixSocket)
		}
	})

	t.Run("honors DEEPLO_UNIX_SOCKET", func(t *testing.T) {
		const custom = "/home/jan/deeplo/deeplo.sock"
		t.Setenv("DEEPLO_UNIX_SOCKET", custom)
		if got := bootstrap.UnixSocketPath(); got != custom {
			t.Errorf("UnixSocketPath() = %q, want %q", got, custom)
		}
	})
}

func TestValidate_UnixSocket_MustEndWithSock(t *testing.T) {
	cases := []string{
		"/home/jan/docker-deploy-prod/deeplo.yml", // exact mistake from production
		"/etc/deeplo/config.yaml",
		"/run/deeplo/settings.json",
		"/run/deeplo/my-socket", // no extension at all
	}
	for _, socket := range cases {
		t.Run(socket, func(t *testing.T) {
			bc := &bootstrap.Config{
				Source:            bootstrap.SourceLocal,
				DataPath:          "/var/lib/deeplo",
				SSHPrivateKeyFile: "/run/secrets/deploy_key",
				SSHPort:           22,
				SSHHostKeyPolicy:  "accept-new",
				UnixSocket:        socket,
			}
			errs := bc.Validate()
			found := false
			for _, e := range errs {
				if e.Field == "DEEPLO_UNIX_SOCKET" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected DEEPLO_UNIX_SOCKET error for %q; got: %v", socket, errs)
			}
		})
	}
}

func TestValidate_UnixSocket_ValidPath(t *testing.T) {
	bc := &bootstrap.Config{
		Source:            bootstrap.SourceLocal,
		DataPath:          "/var/lib/deeplo",
		SSHPrivateKeyFile: "/run/secrets/deploy_key",
		SSHPort:           22,
		SSHHostKeyPolicy:  "accept-new",
		UnixSocket:        "/run/deeplo/deeplo.sock",
	}
	if errs := bc.Validate(); len(errs) != 0 {
		t.Errorf("valid socket path should have no errors; got: %v", errs)
	}
}

// Resolve: local mode

func TestResolve_Local_Success(t *testing.T) {
	path := writeLocalConfig(t, minimalConfigYAML)
	bc := &bootstrap.Config{Source: bootstrap.SourceLocal, ConfigFile: path}

	rc, err := bootstrap.LoadConfig(context.Background(), bc, slog.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rc.Config == nil {
		t.Fatal("expected non-nil Config")
	}
	if rc.FromCache {
		t.Error("local mode should not set FromCache")
	}
}

func TestResolve_Local_MissingFile(t *testing.T) {
	bc := &bootstrap.Config{Source: bootstrap.SourceLocal}
	_, err := bootstrap.LoadConfig(context.Background(), bc, slog.Default())
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

// A commented/empty local config loads without error; validation is deferred so
// a fresh install starts the daemon idle instead of crash-looping.
func TestResolve_Local_EmptyConfigLoadsWithoutValidation(t *testing.T) {
	path := writeLocalConfig(t, "# deeplo config - nothing configured yet\n")
	bc := &bootstrap.Config{Source: bootstrap.SourceLocal, ConfigFile: path}

	rc, err := bootstrap.LoadConfig(context.Background(), bc, slog.Default())
	if err != nil {
		t.Fatalf("LoadConfig on empty config: unexpected error: %v", err)
	}
	if rc.Config == nil {
		t.Fatal("expected non-nil Config for an empty file")
	}
	if len(rc.Config.Hosts) != 0 || len(rc.Config.Repos) != 0 || len(rc.Config.Projects) != 0 {
		t.Errorf("expected an empty config, got %+v", rc.Config)
	}
	// The config is invalid by Validate's rules; the local loader must not enforce that.
	if errs := rc.Config.Validate(); len(errs) == 0 {
		t.Error("expected Validate() to flag the empty config; loader must defer, not enforce")
	}
}

// A partially-filled local config (hosts only) still loads without error.
func TestResolve_Local_IncompleteConfigLoadsWithoutValidation(t *testing.T) {
	path := writeLocalConfig(t, "hosts:\n  - name: h1\n    address: 10.0.0.1\n    deploy_dir: /srv\n")
	bc := &bootstrap.Config{Source: bootstrap.SourceLocal, ConfigFile: path}

	rc, err := bootstrap.LoadConfig(context.Background(), bc, slog.Default())
	if err != nil {
		t.Fatalf("LoadConfig on incomplete config: unexpected error: %v", err)
	}
	if rc.Config == nil || len(rc.Config.Hosts) != 1 {
		t.Fatalf("expected a config with 1 host, got %+v", rc.Config)
	}
}

// Only validation is relaxed for local configs; a parse error remains fatal.
func TestResolve_Local_MalformedYAMLStillErrors(t *testing.T) {
	path := writeLocalConfig(t, "hosts: [unterminated\n")
	bc := &bootstrap.Config{Source: bootstrap.SourceLocal, ConfigFile: path}

	if _, err := bootstrap.LoadConfig(context.Background(), bc, slog.Default()); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

// Resolve: git mode

func TestResolve_Git_FreshFetch(t *testing.T) {
	requireGit(t)
	bare, sha := setupConfigRepo(t, minimalConfigYAML)
	dataPath := t.TempDir()

	bc := &bootstrap.Config{
		Source:           bootstrap.SourceGit,
		ConfigRepoURL:    bare,
		ConfigRepoBranch: "main",
		ConfigRepoFile:   "config.yml",
		DataPath:         dataPath,
	}

	rc, err := bootstrap.LoadConfig(context.Background(), bc, slog.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rc.FromCache {
		t.Error("expected fresh fetch, not cache")
	}
	if rc.CommitSha != sha {
		t.Errorf("CommitSha: got %q, want %q", rc.CommitSha, sha)
	}
	if rc.Config == nil {
		t.Fatal("expected non-nil Config")
	}

	cacheDir := filepath.Join(dataPath, "config")
	if _, err := os.Stat(filepath.Join(cacheDir, "current.yml")); err != nil {
		t.Errorf("expected cache file: %v", err)
	}
	metaRaw, err := os.ReadFile(filepath.Join(cacheDir, "metadata.json"))
	if err != nil {
		t.Fatalf("expected metadata.json to be written: %v", err)
	}
	if !bytes.Contains(metaRaw, []byte(sha)) {
		t.Errorf("metadata.json does not contain the commit SHA %q: %s", sha, metaRaw)
	}
}

func TestResolve_Git_FallbackToCache(t *testing.T) {
	requireGit(t)
	dataPath := t.TempDir()
	cacheDir := filepath.Join(dataPath, "config")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(cacheDir, "current.yml"), []byte(minimalConfigYAML), 0600); err != nil {
		t.Fatal(err)
	}

	bc := &bootstrap.Config{
		Source:           bootstrap.SourceGit,
		ConfigRepoURL:    "/nonexistent/repo.git",
		ConfigRepoBranch: "main",
		ConfigRepoFile:   "config.yml",
		DataPath:         dataPath,
	}

	rc, err := bootstrap.LoadConfig(context.Background(), bc, slog.Default())
	if err != nil {
		t.Fatalf("expected fallback to cache, got error: %v", err)
	}
	if !rc.FromCache {
		t.Error("expected FromCache=true when fetch fails")
	}
	if rc.Config == nil {
		t.Fatal("expected non-nil Config from cache")
	}
	// No metadata file - SHA should be empty, not an error.
	if rc.CommitSha != "" {
		t.Errorf("CommitSha: got %q, want empty (no metadata written)", rc.CommitSha)
	}
}

func TestResolve_Git_FallbackToCache_ReturnsCachedSHA(t *testing.T) {
	requireGit(t)
	dataPath := t.TempDir()
	cacheDir := filepath.Join(dataPath, "config")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	cachedSHA := "aabbccddeeff00112233445566778899aabbccdd"
	if err := os.WriteFile(filepath.Join(cacheDir, "current.yml"), []byte(minimalConfigYAML), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := `{"commit_sha":"` + cachedSHA + `","branch":"main","source_url":"/nonexistent/repo.git","fetched_at":"2024-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(cacheDir, "metadata.json"), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}

	bc := &bootstrap.Config{
		Source:           bootstrap.SourceGit,
		ConfigRepoURL:    "/nonexistent/repo.git",
		ConfigRepoBranch: "main",
		ConfigRepoFile:   "config.yml",
		DataPath:         dataPath,
	}

	rc, err := bootstrap.LoadConfig(context.Background(), bc, slog.Default())
	if err != nil {
		t.Fatalf("expected fallback to cache, got error: %v", err)
	}
	if !rc.FromCache {
		t.Error("expected FromCache=true")
	}
	if rc.CommitSha != cachedSHA {
		t.Errorf("CommitSha: got %q, want %q", rc.CommitSha, cachedSHA)
	}
}

func TestResolve_Git_NoCache_Fails(t *testing.T) {
	requireGit(t)
	dataPath := t.TempDir()

	bc := &bootstrap.Config{
		Source:           bootstrap.SourceGit,
		ConfigRepoURL:    "/nonexistent/repo.git",
		ConfigRepoBranch: "main",
		ConfigRepoFile:   "config.yml",
		DataPath:         dataPath,
	}

	_, err := bootstrap.LoadConfig(context.Background(), bc, slog.Default())
	if err == nil {
		t.Fatal("expected error when fetch fails and no cache exists")
	}
}

func TestResolve_Git_InvalidFetchedConfig_KeepsCache(t *testing.T) {
	requireGit(t)

	// Repo contains an invalid config (version != 1 is a validation error).
	invalidConfig := `version: 99
`
	bare, _ := setupConfigRepo(t, invalidConfig)
	dataPath := t.TempDir()
	cacheDir := filepath.Join(dataPath, "config")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	cachedPath := filepath.Join(cacheDir, "current.yml")
	if err := os.WriteFile(cachedPath, []byte(minimalConfigYAML), 0600); err != nil {
		t.Fatal(err)
	}

	bc := &bootstrap.Config{
		Source:           bootstrap.SourceGit,
		ConfigRepoURL:    bare,
		ConfigRepoBranch: "main",
		ConfigRepoFile:   "config.yml",
		DataPath:         dataPath,
	}

	rc, err := bootstrap.LoadConfig(context.Background(), bc, slog.Default())
	if err != nil {
		t.Fatalf("expected fallback to valid cache; got error: %v", err)
	}
	if !rc.FromCache {
		t.Error("expected FromCache=true when fetched config is invalid")
	}

	// The cached file should still contain the valid config, not the invalid one.
	remaining, readErr := os.ReadFile(cachedPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	cached, _ := config.Parse(remaining)
	if cached == nil || len(cached.Hosts) == 0 {
		t.Error("cache should still hold the previous valid config, not the invalid fetched one")
	}
}

// AppliedConfig

func TestAppliedConfig_NoBaseline(t *testing.T) {
	dataPath := t.TempDir()
	got, err := bootstrap.LoadAppliedConfig(dataPath)
	if err != nil {
		t.Fatalf("LoadAppliedConfig with no baseline: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("LoadAppliedConfig: got %+v, want nil", got)
	}
}

func TestAppliedConfig_RoundTrip(t *testing.T) {
	dataPath := t.TempDir()

	deployConfig := &config.Config{
		Hosts: []config.Host{
			{Name: "host-1", Address: "10.0.0.1", DeployDir: "/srv/apps"},
		},
		Repos: []config.RepoConfig{
			{
				Name:         "app",
				URL:          "git@github.com:org/app.git",
				Branch:       "main",
				TriggerMode:  config.TriggerModeWebhook,
				PollInterval: 60 * time.Second,
			},
		},
		Projects: []config.Project{
			{
				Name:         "myapp",
				Repo:         "app",
				RepoSubdir:   "apps/myapp",
				ComposeFiles: []string{"compose.yml"},
				Targets:      []string{"host-1"},
				DeploySubdir: "myapp",
				WatchPaths:   []string{"apps/myapp/**"},
				PersistFiles: []string{".env"},
			},
		},
	}

	if err := bootstrap.SaveAppliedConfig(dataPath, deployConfig); err != nil {
		t.Fatalf("SaveAppliedConfig: %v", err)
	}

	got, err := bootstrap.LoadAppliedConfig(dataPath)
	if err != nil {
		t.Fatalf("LoadAppliedConfig: %v", err)
	}
	if got == nil {
		t.Fatal("LoadAppliedConfig returned nil after save")
	}
	if !reflect.DeepEqual(deployConfig, got) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", got, deployConfig)
	}
}

func TestAppliedConfig_Overwrite(t *testing.T) {
	dataPath := t.TempDir()

	configV1 := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}},
		Repos:    []config.RepoConfig{{Name: "r1", URL: "git@github.com:org/repo.git", Branch: "main", TriggerMode: config.TriggerModeWebhook, PollInterval: 60 * time.Second}},
		Projects: []config.Project{{Name: "p1", Repo: "r1", RepoSubdir: "apps/p1", ComposeFiles: []string{"compose.yml"}, Targets: []string{"h1"}, DeploySubdir: "p1", WatchPaths: []string{"apps/p1/**"}, PersistFiles: []string{".env"}}},
	}
	configV2 := &config.Config{
		Hosts:    []config.Host{{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"}, {Name: "h2", Address: "10.0.0.2", DeployDir: "/srv"}},
		Repos:    []config.RepoConfig{{Name: "r1", URL: "git@github.com:org/repo.git", Branch: "main", TriggerMode: config.TriggerModeWebhook, PollInterval: 60 * time.Second}},
		Projects: []config.Project{{Name: "p1", Repo: "r1", RepoSubdir: "apps/p1", ComposeFiles: []string{"compose.yml"}, Targets: []string{"h1", "h2"}, DeploySubdir: "p1", WatchPaths: []string{"apps/p1/**"}, PersistFiles: []string{".env"}}},
	}

	if err := bootstrap.SaveAppliedConfig(dataPath, configV1); err != nil {
		t.Fatalf("SaveAppliedConfig v1: %v", err)
	}
	if err := bootstrap.SaveAppliedConfig(dataPath, configV2); err != nil {
		t.Fatalf("SaveAppliedConfig v2: %v", err)
	}

	got, err := bootstrap.LoadAppliedConfig(dataPath)
	if err != nil {
		t.Fatalf("LoadAppliedConfig: %v", err)
	}
	if !reflect.DeepEqual(configV2, got) {
		t.Errorf("expected v2 after overwrite; got %+v", got)
	}
}
