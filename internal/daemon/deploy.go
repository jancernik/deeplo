package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/state"
	"github.com/jancernik/deeplo/internal/utils"
)

// Returns the function injected into the admin API server for
// POST /api/v1/deploy. It resolves the project and target hosts from the
// current config, finds the latest known SHA, and queues a forced deploy.
// The queued deploy runs asynchronously and outlives the HTTP request, so it is
// dispatched on deployCtx rather than the request context.
func buildDeployFunc(
	deployCtx context.Context,
	getConfig func() *config.Config,
	store *state.FileStore,
	getMirrorHead func(repoURL, branch string) (string, bool),
	onDeploy func(context.Context, planner.RepoEvent),
	logger *slog.Logger,
) func(ctx context.Context, projectName, hostName string) ([]string, error) {
	return func(_ context.Context, projectName, hostName string) ([]string, error) {
		deployConfig := getConfig()

		var project *config.Project
		for index := range deployConfig.Projects {
			if deployConfig.Projects[index].Name == projectName {
				project = &deployConfig.Projects[index]
				break
			}
		}
		if project == nil {
			return nil, fmt.Errorf("project %q not found", projectName)
		}

		var repo *config.RepoConfig
		for index := range deployConfig.Repos {
			if deployConfig.Repos[index].Name == project.Repo {
				repo = &deployConfig.Repos[index]
				break
			}
		}
		if repo == nil {
			return nil, fmt.Errorf("repo %q not found for project %q", project.Repo, projectName)
		}

		hostByName := deployConfig.HostIndex()

		var targets []planner.DeployTarget
		for _, targetName := range project.Targets {
			if hostName != "" && targetName != hostName {
				continue
			}
			host, ok := hostByName[targetName]
			if !ok {
				continue
			}
			targets = append(targets, planner.DeployTarget{Project: *project, Host: host, Repo: *repo})
		}

		if hostName != "" && len(targets) == 0 {
			return nil, fmt.Errorf("host %q is not a target of project %q", hostName, projectName)
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("project %q has no configured targets", projectName)
		}

		sha, ok := getMirrorHead(repo.URL, repo.Branch)
		if !ok || sha == "" {
			repoState, err := store.GetRepoState(repo.Name, repo.Branch)
			if err != nil || repoState == nil || repoState.LastDeployedSha == "" {
				return nil, fmt.Errorf("no known commit for repo %q; push a commit first", repo.Name)
			}
			sha = repoState.LastDeployedSha
		}

		targetNames := make([]string, len(targets))
		for index, target := range targets {
			targetNames[index] = target.Project.Name + "/" + target.Host.Name
		}

		logger.Info("queuing manual deploy",
			"project", projectName, "targets", targetNames, "sha", utils.ShortSha(sha))

		onDeploy(deployCtx, planner.RepoEvent{
			Source:        planner.TriggerRedeploy,
			RepoName:      repo.Name,
			Branch:        repo.Branch,
			CommitSha:     sha,
			ForcedTargets: targets,
			Redeploy:      true,
		})

		return targetNames, nil
	}
}
