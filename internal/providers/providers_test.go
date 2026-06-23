package providers

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/webhook"
)

// recordingHandler captures the warning messages emitted during a test.
type recordingHandler struct {
	warnings []string
}

func (handler *recordingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (handler *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Level >= slog.LevelWarn {
		handler.warnings = append(handler.warnings, record.Message)
	}
	return nil
}

func (handler *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return handler }
func (handler *recordingHandler) WithGroup(_ string) slog.Handler      { return handler }

func multiHostConfig() *config.Config {
	return &config.Config{
		Projects: []config.Project{
			{Name: "identity", Targets: []string{"vm-1", "vm-2"}},
			{Name: "nginx", Targets: []string{"vm-1"}},
		},
	}
}

func TestWarnAmbiguousReporting_WarnsForMultiHostProject(t *testing.T) {
	handler := &recordingHandler{}
	env := &bootstrap.Config{GitHubTokenFile: "/run/token", GitHubEnvironmentHost: false}

	WarnAmbiguousReporting(env, multiHostConfig(), slog.New(handler))

	if len(handler.warnings) != 1 {
		t.Fatalf("expected exactly one warning (for the multi-host project), got %d: %v",
			len(handler.warnings), handler.warnings)
	}
}

func TestWarnAmbiguousReporting_SilentWhenEnvironmentHost(t *testing.T) {
	handler := &recordingHandler{}
	env := &bootstrap.Config{GitHubTokenFile: "/run/token", GitHubEnvironmentHost: true}

	WarnAmbiguousReporting(env, multiHostConfig(), slog.New(handler))

	if len(handler.warnings) != 0 {
		t.Errorf("expected no warnings when EnvironmentHost is true, got %v", handler.warnings)
	}
}

func TestWarnAmbiguousReporting_SilentWhenReportingDisabled(t *testing.T) {
	handler := &recordingHandler{}
	env := &bootstrap.Config{GitHubTokenFile: "", GitHubEnvironmentHost: false}

	WarnAmbiguousReporting(env, multiHostConfig(), slog.New(handler))

	if len(handler.warnings) != 0 {
		t.Errorf("expected no warnings when reporting is disabled, got %v", handler.warnings)
	}
}

func repoConfig(mode config.TriggerMode) *config.Config {
	return &config.Config{Repos: []config.RepoConfig{{Name: "app", TriggerMode: mode}}}
}

// Without a webhook secret, a poll-only config needs no webhooks, so no warning.
func TestRegisterWebhooks_SilentWhenPollOnly(t *testing.T) {
	handler := &recordingHandler{}
	env := &bootstrap.Config{GitHubWebhookSecretFile: ""}
	noop := func(context.Context, webhook.PushEvent) {}

	if err := RegisterWebhooks(http.NewServeMux(), env, repoConfig(config.TriggerModePoll), context.Background(), noop, slog.New(handler)); err != nil {
		t.Fatalf("RegisterWebhooks: %v", err)
	}
	if len(handler.warnings) != 0 {
		t.Errorf("poll-only config should not warn, got %v", handler.warnings)
	}
}

// Without a webhook secret, a webhook or hybrid repo warns that deploys won't trigger.
func TestRegisterWebhooks_WarnsWhenWebhookRepoUnconfigured(t *testing.T) {
	for _, mode := range []config.TriggerMode{config.TriggerModeWebhook, config.TriggerModeHybrid} {
		handler := &recordingHandler{}
		env := &bootstrap.Config{GitHubWebhookSecretFile: ""}
		noop := func(context.Context, webhook.PushEvent) {}

		if err := RegisterWebhooks(http.NewServeMux(), env, repoConfig(mode), context.Background(), noop, slog.New(handler)); err != nil {
			t.Fatalf("RegisterWebhooks(%s): %v", mode, err)
		}
		if len(handler.warnings) != 1 {
			t.Errorf("%s repo without secret should warn once, got %v", mode, handler.warnings)
		}
	}
}

func TestWarnAmbiguousReporting_SilentForSingleHostProjects(t *testing.T) {
	handler := &recordingHandler{}
	env := &bootstrap.Config{GitHubTokenFile: "/run/token", GitHubEnvironmentHost: false}
	deployConfig := &config.Config{
		Projects: []config.Project{
			{Name: "nginx", Targets: []string{"vm-1"}},
			{Name: "adminer", Targets: []string{"vm-2"}},
		},
	}

	WarnAmbiguousReporting(env, deployConfig, slog.New(handler))

	if len(handler.warnings) != 0 {
		t.Errorf("expected no warnings for single-host projects, got %v", handler.warnings)
	}
}
