// Package providers wires concrete webhook and reporting implementations to
// the provider-agnostic interfaces used by the rest of the daemon.
package providers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/reporter"
	githubreport "github.com/jancernik/deeplo/internal/reporter/github"
	"github.com/jancernik/deeplo/internal/webhook"
	githubwebhook "github.com/jancernik/deeplo/internal/webhook/github"
)

// Constructs the active reporter from the bootstrap config.
// Returns a noop reporter when no provider is configured.
func BuildReporter(env *bootstrap.Config, logger *slog.Logger) (reporter.Reporter, error) {
	if env.GitHubTokenFile != "" {
		rep, err := githubreport.New(githubreport.Config{
			TokenFile:       env.GitHubTokenFile,
			Environment:     env.GitHubEnvironment,
			EnvironmentHost: env.GitHubEnvironmentHost,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("github reporter: %w", err)
		}
		if env.GitHubEnvironment != "" {
			logger.Info("github reporting enabled", "environment", env.GitHubEnvironment)
		} else {
			logger.Info("github reporting enabled")
		}
		return rep, nil
	}
	return reporter.Noop(), nil
}

func WarnAmbiguousReporting(env *bootstrap.Config, deployConfig *config.Config, logger *slog.Logger) {
	if env.GitHubTokenFile == "" || env.GitHubEnvironmentHost || deployConfig == nil {
		return
	}
	for _, project := range deployConfig.Projects {
		if len(project.Targets) > 1 {
			logger.Warn("github reporting collides for multi-host project: only the last host stays active, set DEEPLO_GITHUB_ENVIRONMENT_HOST=true to give each host its own environment",
				"project", project.Name, "hosts", len(project.Targets))
		}
	}
}

func RegisterWebhooks(
	mux *http.ServeMux,
	env *bootstrap.Config,
	deployConfig *config.Config,
	appCtx context.Context,
	onPush func(context.Context, webhook.PushEvent),
	logger *slog.Logger,
) error {
	if env.GitHubWebhookSecretFile == "" {
		if anyRepoUsesWebhooks(deployConfig) {
			logger.Warn("github webhook handler not registered, set DEEPLO_GITHUB_WEBHOOK_SECRET_FILE to enable webhook-triggered deploys")
		}
		return nil
	}
	handler, err := githubwebhook.NewHandler(appCtx, env.GitHubWebhookSecretFile, nil, onPush, logger)
	if err != nil {
		return fmt.Errorf("github webhook handler: %w", err)
	}
	mux.Handle("/webhooks/github", handler)
	logger.Info("github webhook handler registered", "path", "/webhooks/github")
	return nil
}

func anyRepoUsesWebhooks(deployConfig *config.Config) bool {
	if deployConfig == nil {
		return false
	}
	for _, repo := range deployConfig.Repos {
		if repo.TriggerMode == config.TriggerModeWebhook || repo.TriggerMode == config.TriggerModeHybrid {
			return true
		}
	}
	return false
}
