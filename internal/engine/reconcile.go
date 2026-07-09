package engine

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/compose"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/runner"
	"github.com/jancernik/deeplo/internal/ssh"
	"github.com/jancernik/deeplo/internal/state"
	"github.com/jancernik/deeplo/internal/utils"
)

type TeardownTarget struct {
	ProjectName  string
	Host         config.Host
	RemoteDir    string
	ComposeFiles []string
	RemoveState  bool
}

// Compares oldConfig to newConfig and returns every project-host pair that needs to be torn down.
func FindTeardownTargets(oldConfig, newConfig *config.Config) []TeardownTarget {
	newProjects := newConfig.ProjectIndex()
	oldHosts := oldConfig.HostIndex()
	newHosts := newConfig.HostIndex()

	var targets []TeardownTarget

	for _, oldProject := range oldConfig.Projects {
		newProject, stillInConfig := newProjects[oldProject.Name]

		newTargetHosts := make(map[string]bool)
		if stillInConfig {
			for _, hostName := range newProject.Targets {
				newTargetHosts[hostName] = true
			}
		}

		for _, hostName := range oldProject.Targets {
			oldHost, ok := oldHosts[hostName]
			if !ok {
				continue
			}
			oldRemoteDir := path.Join(oldHost.DeployDir, oldProject.DeploySubdir)

			stillTargeted := stillInConfig && newTargetHosts[hostName]
			sameLocation := false
			if stillTargeted {
				if newHost, ok := newHosts[hostName]; ok {
					sameLocation = path.Join(newHost.DeployDir, newProject.DeploySubdir) == oldRemoteDir
				}
			}
			if sameLocation {
				continue
			}

			targets = append(targets, TeardownTarget{
				ProjectName:  oldProject.Name,
				Host:         oldHost,
				RemoteDir:    oldRemoteDir,
				ComposeFiles: oldProject.ComposeFiles,
				RemoveState:  !stillTargeted,
			})
		}
	}

	return targets
}

// Tears down every target returned by FindTeardownTargets that has a deployment record.
// Each teardown is level-triggered, if the target was re-added the teardown is skipped.
func ReconcileRemovals(
	ctx context.Context,
	oldConfig, newConfig *config.Config,
	env *bootstrap.Config,
	store *state.FileStore,
	dialer ssh.Dialer,
	jobRunner *runner.Runner,
	getConfig func() *config.Config,
	logger *slog.Logger,
) {
	targets := FindTeardownTargets(oldConfig, newConfig)
	if len(targets) == 0 {
		return
	}

	logger.Info("removing deleted targets", "targets", len(targets))

	var wg sync.WaitGroup
	for _, target := range targets {
		if !hasDeploymentRecord(store, target.ProjectName, target.Host.Name) {
			logger.Debug("no deployment record, skipping removal", "project", target.ProjectName, "host", target.Host.Name)
			continue
		}

		teardownTarget := target
		log := logger.With("project", teardownTarget.ProjectName, "host", teardownTarget.Host.Name)
		wg.Add(1)
		job := runner.Job{
			ID:      "teardown:" + teardownTarget.ProjectName + "/" + teardownTarget.Host.Name,
			Project: teardownTarget.ProjectName,
			Host:    teardownTarget.Host.Name,
			DeployFunc: func(ctx context.Context) error {
				if dir, ok := desiredRemoteDir(getConfig(), teardownTarget.ProjectName, teardownTarget.Host.Name); ok && dir == teardownTarget.RemoteDir {
					log.Info("target re-added before removal ran, skipping removal", "dir", teardownTarget.RemoteDir)
					return nil
				}
				log.Info("removing deleted target", "dir", teardownTarget.RemoteDir)
				if err := teardown(ctx, env, teardownTarget, dialer, log); err != nil {
					log.Warn("removal failed", "err", err)
					return err
				}
				log.Info("removal complete")
				if teardownTarget.RemoveState {
					if _, err := store.DeleteLatestDeployment(teardownTarget.ProjectName, teardownTarget.Host.Name); err != nil {
						log.Warn("could not clear deployment state after teardown", "err", err)
					}
				}
				return nil
			},
			OnComplete: func(runner.Result) { wg.Done() },
		}
		if err := jobRunner.Submit(ctx, job); err != nil {
			log.Warn("failed to submit teardown job", "err", err)
			wg.Done()
		}
	}
	wg.Wait()
}

// Connects to the host, stops the compose project, and removes its directory.
// The directory is removed unconditionally after a successful down
func teardown(
	ctx context.Context,
	env *bootstrap.Config,
	target TeardownTarget,
	dialer ssh.Dialer,
	logger *slog.Logger,
) error {
	conn, err := dialer.Dial(ctx, ssh.DialConfig{
		Address:        target.Host.Address,
		Port:           target.Host.EffectivePort(env.SSHPort),
		User:           target.Host.EffectiveUser(env.SSHUser),
		PrivateKeyFile: env.SSHPrivateKeyFile,
		KnownHostsFile: env.SSHKnownHosts,
		HostKeyPolicy:  env.SSHHostKeyPolicy,
	})
	if err != nil {
		return fmt.Errorf("dial %s (%s): %w", target.Host.Name, target.Host.Address, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			logger.Warn("close SSH connection", "err", err)
		}
	}()

	executor := compose.NewExecutor(conn, target.RemoteDir, target.ProjectName, logger)
	if err := executor.Down(ctx, target.ComposeFiles); err != nil {
		return fmt.Errorf("compose down: %w", err)
	}

	rmCmd := "rm -rf '" + strings.ReplaceAll(target.RemoteDir, "'", `'\''`) + "'"
	if _, _, err := conn.Run(ctx, rmCmd); err != nil {
		return fmt.Errorf("remove directory %s: %w", target.RemoteDir, err)
	}

	return nil
}

func hasDeploymentRecord(store *state.FileStore, project, host string) bool {
	if store == nil {
		return false
	}
	deployment, err := store.GetLatestDeployment(project, host)
	return err == nil && deployment != nil
}

// Compares oldConfig to newConfig and returns the full DeployTarget objects for every new project-host pair.
func FindAddedDeployTargets(oldConfig, newConfig *config.Config) []planner.DeployTarget {
	type pair struct{ project, host string }
	oldPairs := make(map[pair]bool)
	for _, project := range oldConfig.Projects {
		for _, hostName := range project.Targets {
			oldPairs[pair{project.Name, hostName}] = true
		}
	}

	var targets []planner.DeployTarget
	for _, target := range planner.AllTargets(newConfig) {
		if oldPairs[pair{target.Project.Name, target.Host.Name}] {
			continue
		}
		targets = append(targets, target)
	}
	return targets
}

// Dispatches a deploy event for every repo that gained new project-host pairs.
// Only the new targets are deployed, without redeploying existing targets.
func ReconcileAdditions(
	ctx context.Context,
	oldConfig, newConfig *config.Config,
	store *state.FileStore,
	getMirrorHead func(repoURL, branch string) (string, bool),
	onDeploy func(context.Context, planner.RepoEvent),
	logger *slog.Logger,
) {
	if store == nil {
		return
	}
	targets := FindAddedDeployTargets(oldConfig, newConfig)
	if len(targets) == 0 {
		return
	}

	targetsByRepo := make(map[string][]planner.DeployTarget)
	for _, target := range targets {
		targetsByRepo[target.Repo.Name] = append(targetsByRepo[target.Repo.Name], target)
	}

	for repoName, repoTargets := range targetsByRepo {
		repoConfig := repoTargets[0].Repo

		sha, ok := getMirrorHead(repoConfig.URL, repoConfig.Branch)
		if !ok || sha == "" {
			repoState, err := store.GetRepoState(repoName, repoConfig.Branch)
			if err != nil || repoState == nil || repoState.LastDeployedSha == "" {
				logger.Debug("no known commit for repo, skipping new target deploys",
					"repo", repoName, "branch", repoConfig.Branch)
				continue
			}
			sha = repoState.LastDeployedSha
		}

		logger.Debug("dispatching deploy for newly added targets",
			"repo", repoName, "branch", repoConfig.Branch, "sha", utils.ShortSha(sha),
			"targets", len(repoTargets))
		onDeploy(ctx, planner.RepoEvent{
			Source:        planner.TriggerReconcileAddition,
			RepoName:      repoName,
			Branch:        repoConfig.Branch,
			CommitSha:     sha,
			ForcedTargets: repoTargets,
		})
	}
}

func deployConfigChanged(oldProject, newProject config.Project) bool {
	return oldProject.RepoSubdir != newProject.RepoSubdir ||
		oldProject.DeploySubdir != newProject.DeploySubdir ||
		!slices.Equal(oldProject.ComposeFiles, newProject.ComposeFiles) ||
		!slices.Equal(oldProject.ExtraFiles, newProject.ExtraFiles) ||
		!slices.Equal(oldProject.PersistFiles, newProject.PersistFiles)
}

func hostConfigChanged(oldHost, newHost config.Host) bool {
	return oldHost.Address != newHost.Address ||
		oldHost.DeployDir != newHost.DeployDir ||
		oldHost.User != newHost.User ||
		oldHost.Port != newHost.Port
}

// Dispatches a redeploy for every existing project-host pair whose
// deploy configuration changed between oldConfig and newConfig.
func ReconcileProjectChanges(
	ctx context.Context,
	oldConfig, newConfig *config.Config,
	store *state.FileStore,
	onDeploy func(context.Context, planner.RepoEvent),
	logger *slog.Logger,
) {
	if store == nil {
		return
	}

	oldProjects := oldConfig.ProjectIndex()
	oldHosts := oldConfig.HostIndex()
	repoByName := newConfig.RepoIndex()
	hostByName := newConfig.HostIndex()

	targetsByRepo := make(map[string][]planner.DeployTarget)

	for _, newProject := range newConfig.Projects {
		oldProject, existed := oldProjects[newProject.Name]
		if !existed {
			continue
		}
		projectChanged := deployConfigChanged(oldProject, newProject)

		repo, ok := repoByName[newProject.Repo]
		if !ok {
			continue
		}

		oldTargetSet := make(map[string]bool, len(oldProject.Targets))
		for _, hostName := range oldProject.Targets {
			oldTargetSet[hostName] = true
		}

		for _, hostName := range newProject.Targets {
			if !oldTargetSet[hostName] {
				continue
			}
			host, ok := hostByName[hostName]
			if !ok {
				continue
			}
			oldHost, hostExisted := oldHosts[hostName]
			hostChanged := hostExisted && hostConfigChanged(oldHost, host)
			if !projectChanged && !hostChanged {
				continue
			}
			targetsByRepo[repo.Name] = append(targetsByRepo[repo.Name], planner.DeployTarget{
				Project: newProject,
				Host:    host,
				Repo:    repo,
			})
		}
	}

	if len(targetsByRepo) == 0 {
		return
	}

	logger.Info("redeploying projects with changed config", "repos", len(targetsByRepo))

	for repoName, repoTargets := range targetsByRepo {
		repoConfig := repoTargets[0].Repo
		repoState, err := store.GetRepoState(repoName, repoConfig.Branch)
		if err != nil || repoState == nil || repoState.LastDeployedSha == "" {
			logger.Warn("no known commit for repo, skipping changed-config redeploy",
				"repo", repoName, "branch", repoConfig.Branch)
			continue
		}
		logger.Info("dispatching redeploy for projects with changed config",
			"repo", repoName, "branch", repoConfig.Branch, "sha", utils.ShortSha(repoState.LastDeployedSha),
			"targets", len(repoTargets))
		onDeploy(ctx, planner.RepoEvent{
			Source:        planner.TriggerReconcileProjectChange,
			RepoName:      repoName,
			Branch:        repoConfig.Branch,
			CommitSha:     repoState.LastDeployedSha,
			ForcedTargets: repoTargets,
			Redeploy:      true,
		})
	}
}
