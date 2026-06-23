package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/reporter"
	"github.com/jancernik/deeplo/internal/runner"
	"github.com/jancernik/deeplo/internal/ssh"
	"github.com/jancernik/deeplo/internal/state"
	"github.com/jancernik/deeplo/internal/utils"
	"github.com/jancernik/deeplo/internal/webhook"
)

// Returns the onDeploy callback shared by the webhook handler and the poller.
func MakePushHandler(
	getConfig func() *config.Config,
	env *bootstrap.Config,
	deployRunner *runner.Runner,
	dialer ssh.Dialer,
	store *state.FileStore,
	deployReporter reporter.Reporter,
	sshEnv []string,
	logger *slog.Logger,
) func(context.Context, planner.RepoEvent) {
	return func(ctx context.Context, event planner.RepoEvent) {
		log := logger.With(
			"repo", event.RepoName,
			"branch", event.Branch,
			"sha", utils.ShortSha(event.CommitSha),
			"source", event.Source,
		)

		targets := planner.Plan(getConfig(), event)
		if len(targets) == 0 {
			log.Info("push event matched no deploy targets")
			return
		}

		log.Info("push event: queueing deploy targets", "targets", len(targets))

		var (
			resultsMutex sync.Mutex
			results      []runner.Result
			waitGroup    sync.WaitGroup
		)

		for _, target := range targets {
			if !event.Redeploy && ShouldSkipDeploy(store, target.Project.Name, target.Host.Name, event.CommitSha) {
				log.Info("deploy dedup: already deployed, skipping",
					"project", target.Project.Name,
					"host", target.Host.Name,
				)
				continue
			}

			waitGroup.Add(1)
			jobID := fmt.Sprintf("%s/%s@%s", target.Project.Name, target.Host.Name, utils.ShortSha(event.CommitSha))

			job := runner.Job{
				ID:      jobID,
				Project: target.Project.Name,
				Host:    target.Host.Name,
				DeployFunc: func(ctx context.Context) error {
					if !TargetDesired(getConfig(), target.Project.Name, target.Host.Name) {
						log.Info("target no longer in config, skipping deploy",
							"project", target.Project.Name, "host", target.Host.Name)
						return nil
					}
					if !event.Redeploy && ShouldSkipDeploy(store, target.Project.Name, target.Host.Name, event.CommitSha) {
						log.Info("deploy dedup: already deployed, skipping",
							"project", target.Project.Name, "host", target.Host.Name)
						return nil
					}
					if env.DeployTimeout > 0 {
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, env.DeployTimeout)
						defer cancel()
					}
					return DeployTarget(ctx, env, target, event, sshEnv, dialer, store, deployReporter, logger)
				},
				OnComplete: func(result runner.Result) {
					resultsMutex.Lock()
					results = append(results, result)
					resultsMutex.Unlock()
					waitGroup.Done()
				},
			}

			if err := deployRunner.Submit(ctx, job); err != nil {
				log.Error("failed to submit deploy job", "job", jobID, "err", err)
				waitGroup.Done()
			}
		}

		go func() {
			waitGroup.Wait()
			resultsMutex.Lock()
			defer resultsMutex.Unlock()
			if len(results) == 0 {
				return
			}
			var failed int
			for _, result := range results {
				if result.Err != nil {
					failed++
				}
			}
			if failed > 0 {
				log.Error("deploy summary: failures", "total", len(results), "failed", failed)
			} else {
				log.Info("deploy summary: all succeeded", "total", len(results))
			}
		}()
	}
}

// Returns the callback passed to the webhook handler.
func MakeWebhookPushHandler(
	getConfig func() *config.Config,
	store *state.FileStore,
	onDeploy func(context.Context, planner.RepoEvent),
	logger *slog.Logger,
) func(context.Context, webhook.PushEvent) {
	var mutex sync.Mutex
	var lastConfig *config.Config
	var repoByFullName map[string]config.RepoConfig

	return func(ctx context.Context, push webhook.PushEvent) {
		log := logger.With("repo_full_name", push.RepoFullName, "branch", push.Branch)

		mutex.Lock()
		currentConfig := getConfig()
		if currentConfig != lastConfig {
			lastConfig = currentConfig
			repoByFullName = BuildRepoFullNameIndex(currentConfig.Repos)
		}
		index := repoByFullName
		mutex.Unlock()

		repo, ok := index[push.RepoFullName]
		if !ok {
			log.Warn("no repo configured for repository")
			return
		}
		if push.Branch != repo.Branch {
			log.Debug("branch mismatch, skipping",
				"repo", repo.Name,
				"configured_branch", repo.Branch,
			)
			return
		}
		if repo.TriggerMode != config.TriggerModeWebhook && repo.TriggerMode != config.TriggerModeHybrid {
			log.Debug("repo is not webhook-enabled, skipping",
				"repo", repo.Name,
				"trigger_mode", repo.TriggerMode,
			)
			return
		}

		changedFiles := push.ChangedFiles
		if store != nil {
			repoState, err := store.GetRepoState(repo.Name, push.Branch)
			if err != nil {
				log.Warn("failed to read repo state", "repo", repo.Name, "err", err)
			} else {
				firstSeen := repoState == nil || repoState.LastDeployedSha == ""
				if repoState == nil {
					repoState = &state.RepoState{
						Repo:   repo.Name,
						Branch: push.Branch,
					}
				}
				repoState.LastSeenSha = push.CommitSha
				repoState.LastDeployedSha = push.CommitSha
				repoState.LastDeliveryID = push.DeliveryID
				repoState.TriggerSource = string(planner.TriggerWebhook)
				if err := store.SaveRepoState(repoState); err != nil {
					log.Warn("failed to save repo state", "repo", repo.Name, "err", err)
				}
				if firstSeen {
					changedFiles = nil
				}
			}
		}

		onDeploy(ctx, planner.RepoEvent{
			Source:       planner.TriggerWebhook,
			DeliveryID:   push.DeliveryID,
			RepoName:     repo.Name,
			Branch:       push.Branch,
			CommitSha:    push.CommitSha,
			ChangedFiles: changedFiles,
		})
	}
}

// Extracts the "owner/repo" full name from each repo's URL and maps it to the RepoConfig.
func BuildRepoFullNameIndex(repos []config.RepoConfig) map[string]config.RepoConfig {
	index := make(map[string]config.RepoConfig, len(repos))
	for _, repo := range repos {
		if fullName := RepoFullName(repo.URL); fullName != "" {
			index[fullName] = repo
		}
	}
	return index
}

// Extracts "owner/repo" from a git clone URL.
func RepoFullName(repoURL string) string {
	normalizedURL := strings.TrimSuffix(repoURL, ".git")
	for _, separator := range []string{"github.com/", "github.com:"} {
		if _, after, ok := strings.Cut(normalizedURL, separator); ok {
			return after
		}
	}
	parts := strings.FieldsFunc(normalizedURL, func(character rune) bool { return character == '/' || character == ':' })
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return normalizedURL
}

// Drains the runner's results channel so completed jobs do not block on send.
func DrainResults(results <-chan runner.Result) {
	for range results {
	}
}
