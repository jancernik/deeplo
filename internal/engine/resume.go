package engine

import (
	"context"
	"log/slog"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/state"
	"github.com/jancernik/deeplo/internal/utils"
)

// The subset of *mirror.Repo resume needs to check what changed between commits.
type MirrorDiffer interface {
	HasCommit(ctx context.Context, sha string) bool
	IsAncestor(ctx context.Context, ancestor, descendant string) bool
	DiffFiles(ctx context.Context, oldSha, newSha string) ([]string, error)
}

// Recovers targets whose deploy jobs were dropped when the daemon stopped
// mid-fan-out. A target is resumed only when the commits since its last successful
// deploy touched its watched paths, so unrelated projects are left untouched.
func ResumeIncompleteDeploys(
	ctx context.Context,
	deployConfig *config.Config,
	store *state.FileStore,
	getMirrorHead func(repoURL, branch string) (string, bool),
	findMirror func(repoURL string) MirrorDiffer,
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

		var repoMirror MirrorDiffer
		if findMirror != nil {
			repoMirror = findMirror(repoConfig.URL)
		}

		var pending []planner.DeployTarget
		for _, target := range repoTargets {
			if ShouldSkipDeploy(store, target.Project.Name, target.Host.Name, sha) {
				continue
			}
			if targetUntouchedSinceLastDeploy(ctx, store, repoMirror, target, sha) {
				logger.Debug("commits since last deploy did not touch target, skipping resume",
					"repo", repoName, "project", target.Project.Name, "host", target.Host.Name)
				continue
			}
			pending = append(pending, target)
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

// Reports whether the range from the target's last successful deploy to headSha
// provably did not touch its watched paths. Cases it can't prove (no success
// baseline, missing or diverged commits, diff errors) return false to keep the
// target, so a resume never silently drops work.
func targetUntouchedSinceLastDeploy(
	ctx context.Context,
	store *state.FileStore,
	repoMirror MirrorDiffer,
	target planner.DeployTarget,
	headSha string,
) bool {
	if repoMirror == nil {
		return false
	}
	latest, err := store.GetLatestDeployment(target.Project.Name, target.Host.Name)
	if err != nil || latest == nil || latest.Status != state.StatusSuccess {
		return false
	}
	deployedSha := latest.CommitSha
	if deployedSha == "" || deployedSha == headSha {
		return false
	}
	if !repoMirror.HasCommit(ctx, deployedSha) || !repoMirror.HasCommit(ctx, headSha) {
		return false
	}
	if !repoMirror.IsAncestor(ctx, deployedSha, headSha) {
		return false
	}
	files, err := repoMirror.DiffFiles(ctx, deployedSha, headSha)
	if err != nil {
		return false
	}
	return !planner.ProjectMatchesChangedFiles(target.Project, files)
}
