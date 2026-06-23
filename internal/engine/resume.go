package engine

import (
	"context"
	"log/slog"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/state"
	"github.com/jancernik/deeplo/internal/utils"
)

// Brings every configured target up to its repo's last-known commit when not
// already deployed there, recovering targets whose queued jobs were dropped when
// the daemon stopped after a push had deployed only some of its targets.
func ResumeIncompleteDeploys(
	ctx context.Context,
	deployConfig *config.Config,
	store *state.FileStore,
	getMirrorHead func(repoURL, branch string) (string, bool),
	onDeploy func(context.Context, planner.RepoEvent),
	logger *slog.Logger,
) {
	if store == nil || deployConfig == nil {
		return
	}

	targetsByRepo := make(map[string][]planner.DeployTarget)
	for _, target := range planner.AllTargets(deployConfig) {
		targetsByRepo[target.Repo.Name] = append(targetsByRepo[target.Repo.Name], target)
	}

	for repoName, repoTargets := range targetsByRepo {
		repoConfig := repoTargets[0].Repo

		sha, ok := getMirrorHead(repoConfig.URL, repoConfig.Branch)
		if !ok || sha == "" {
			repoState, err := store.GetRepoState(repoName, repoConfig.Branch)
			if err != nil || repoState == nil || repoState.LastDeployedSha == "" {
				logger.Debug("no known commit for repo, skipping resume",
					"repo", repoName, "branch", repoConfig.Branch)
				continue
			}
			sha = repoState.LastDeployedSha
		}

		var pending []planner.DeployTarget
		for _, target := range repoTargets {
			if !ShouldSkipDeploy(store, target.Project.Name, target.Host.Name, sha) {
				pending = append(pending, target)
			}
		}
		if len(pending) == 0 {
			continue
		}

		logger.Info("resuming incomplete deploys",
			"repo", repoName, "branch", repoConfig.Branch, "sha", utils.ShortSha(sha),
			"targets", len(pending))
		onDeploy(ctx, planner.RepoEvent{
			Source:        planner.TriggerResume,
			RepoName:      repoName,
			Branch:        repoConfig.Branch,
			CommitSha:     sha,
			ForcedTargets: pending,
		})
	}
}
