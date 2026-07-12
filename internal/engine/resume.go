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

type MirrorRepo interface {
	MirrorDiffer
	EnsureCommit(ctx context.Context, sha string) error
}

// Recovers targets whose deploy jobs were dropped when the daemon stopped
// mid-fan-out. A target is resumed only when the commits since its last successful
// deploy touched its watched paths, so unrelated projects are left untouched.
func ResumeIncompleteDeploys(
	ctx context.Context,
	deployConfig *config.Config,
	store *state.FileStore,
	resolveMirror func(repoURL, branch string) (MirrorRepo, string, bool),
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

		repoMirror, sha, ok := resolveMirror(repoConfig.URL, repoConfig.Branch)
		if !ok || sha == "" {
			repoState, err := store.GetRepoState(repoName, repoConfig.Branch)
			if err != nil || repoState == nil || repoState.LastDeployedSha == "" {
				logger.Debug("no known commit for repo, skipping resume",
					"repo", repoName, "branch", repoConfig.Branch)
				continue
			}
			sha = repoState.LastDeployedSha
		}

		if repoMirror != nil && !repoMirror.HasCommit(ctx, sha) {
			if err := repoMirror.EnsureCommit(ctx, sha); err != nil {
				logger.Warn("could not fetch head commit for resume; targets may over-deploy",
					"repo", repoName, "sha", utils.ShortSha(sha), "err", err)
			}
		}

		pending := PendingTargetsForHead(ctx, store, repoTargets, sha, repoMirror, logger)
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

// Returns the subset of targets that must be redeployed to reach headSha.
func PendingTargetsForHead(
	ctx context.Context,
	store *state.FileStore,
	targets []planner.DeployTarget,
	headSha string,
	repoMirror MirrorDiffer,
	logger *slog.Logger,
) []planner.DeployTarget {
	var pending []planner.DeployTarget
	for _, target := range targets {
		if ShouldSkipDeploy(store, target.Project.Name, target.Host.Name, headSha) {
			continue
		}
		if targetUntouchedSinceLastDeploy(ctx, store, repoMirror, target, headSha, logger) {
			logger.Debug("commits since last deploy did not touch target, skipping",
				"repo", target.Repo.Name, "project", target.Project.Name, "host", target.Host.Name)
			continue
		}
		pending = append(pending, target)
	}
	return pending
}

// Reports whether the files differing between the target's last successful deploy
// and headSha provably don't touch its watched paths. Cases it can't prove (no
// success baseline, a missing commit, a diff error) return false to keep the target,
// so a resume never silently drops work.
func targetUntouchedSinceLastDeploy(
	ctx context.Context,
	store *state.FileStore,
	repoMirror MirrorDiffer,
	target planner.DeployTarget,
	headSha string,
	logger *slog.Logger,
) bool {
	keep := func(reason string, args ...any) bool {
		args = append([]any{
			"repo", target.Repo.Name, "project", target.Project.Name, "host", target.Host.Name,
			"reason", reason,
		}, args...)
		logger.Debug("target kept for redeploy", args...)
		return false
	}
	if repoMirror == nil {
		return keep("no local mirror to diff against")
	}
	latest, err := store.GetLatestDeployment(target.Project.Name, target.Host.Name)
	if err != nil || latest == nil || latest.Status != state.StatusSuccess {
		return keep("no successful deploy baseline")
	}
	deployedSha := latest.CommitSha
	if deployedSha == "" || deployedSha == headSha {
		return keep("baseline sha empty or equal to head")
	}
	if !repoMirror.HasCommit(ctx, deployedSha) || !repoMirror.HasCommit(ctx, headSha) {
		return keep("baseline or head commit missing from mirror")
	}
	files, err := repoMirror.DiffFiles(ctx, deployedSha, headSha)
	if err != nil {
		return keep("diff failed", "err", err)
	}
	if len(files) == 0 {
		return true
	}
	if planner.ProjectMatchesChangedFiles(target.Project, files) {
		return keep("commits since last deploy touched watched paths")
	}
	return true
}
