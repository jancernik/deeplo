package daemon

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/jancernik/deeplo/internal/config"
)

// TestWarnIncompleteConfig_WarnsForMissingInfrastructure verifies the daemon
// surfaces missing infrastructure (hosts, repos) as warnings rather than
// aborting. A missing-projects gap is NOT warned about - zero projects is a
// valid "deploy nothing" state.
func TestWarnIncompleteConfig_WarnsForMissingInfrastructure(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// An empty config is missing hosts and repos.
	warnIncompleteConfig(logger, &config.Config{})

	out := buf.String()
	for _, field := range []string{"hosts", "repos"} {
		if !strings.Contains(out, "field="+field) {
			t.Errorf("expected a warning naming %q, got:\n%s", field, out)
		}
	}
	if strings.Contains(out, "field=projects") {
		t.Errorf("missing projects must NOT be warned about (valid deploy-nothing state):\n%s", out)
	}
}

// TestWarnIncompleteConfig_SilentForHostsAndReposNoProjects verifies that a
// deliberate "deploy nothing" config (valid hosts and repos, zero projects)
// produces no warnings.
func TestWarnIncompleteConfig_SilentForHostsAndReposNoProjects(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	warnIncompleteConfig(logger, &config.Config{
		Hosts: []config.Host{{Name: "h1", Address: "1.2.3.4", DeployDir: "/srv"}},
		Repos: []config.RepoConfig{{Name: "r1", URL: "git@github.com:o/r.git", Branch: "main", TriggerMode: config.TriggerModeWebhook}},
	})

	if buf.Len() != 0 {
		t.Errorf("expected no warnings for a hosts+repos no-projects config, got:\n%s", buf.String())
	}
}

// TestWarnIncompleteConfig_SilentForValidConfig verifies a complete config
// produces no warnings.
func TestWarnIncompleteConfig_SilentForValidConfig(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	valid := &config.Config{
		Hosts: []config.Host{{Name: "h1", Address: "1.2.3.4", DeployDir: "/srv"}},
		Repos: []config.RepoConfig{{Name: "r1", URL: "git@github.com:o/r.git", Branch: "main", TriggerMode: config.TriggerModeWebhook}},
		Projects: []config.Project{{
			Name: "p1", Repo: "r1", RepoSubdir: "sub",
			ComposeFiles: []string{"docker-compose.yml"}, Targets: []string{"h1"},
		}},
	}
	warnIncompleteConfig(logger, valid)

	if buf.Len() != 0 {
		t.Errorf("expected no warnings for a valid config, got:\n%s", buf.String())
	}
}
