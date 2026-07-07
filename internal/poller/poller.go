// Package poller handles the deploy logic for poll-triggered repositories.
// Scheduling is delegated to the repowatcher package.
package poller

import (
	"context"
	"log/slog"
	"time"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/mirror"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/repowatcher"
	"github.com/jancernik/deeplo/internal/state"
	"github.com/jancernik/deeplo/internal/utils"
)

type repoOpener interface {
	HasCommit(ctx context.Context, sha string) bool
	EnsureCommit(ctx context.Context, sha string) error
	DiffFiles(ctx context.Context, oldSha, newSha string) ([]string, error)
	IsAncestor(ctx context.Context, ancestor, descendant string) bool
}

type Poller struct {
	deployConfig     *config.Config
	dataPath         string
	store            *state.FileStore
	onDeploy         func(context.Context, planner.RepoEvent)
	reloadConfigRepo func(ctx context.Context, repoName string) error
	logger           *slog.Logger
	findRepo         func(url string) (repoOpener, error)
	findConfigMirror func(url string) (repoOpener, error) // nil when no config mirror exists
	sshEnv           []string
}

func New(
	deployConfig *config.Config,
	dataPath string,
	configMirrorDataPath string,
	store *state.FileStore,
	onDeploy func(context.Context, planner.RepoEvent),
	reloadConfigRepo func(ctx context.Context, repoName string) error,
	logger *slog.Logger,
	sshEnv []string,
) *Poller {
	poller := &Poller{
		deployConfig:     deployConfig,
		dataPath:         dataPath,
		store:            store,
		onDeploy:         onDeploy,
		reloadConfigRepo: reloadConfigRepo,
		logger:           logger.With("component", "poller"),
		sshEnv:           sshEnv,
	}
	poller.findRepo = func(url string) (repoOpener, error) {
		repo, err := mirror.Find(url, poller.dataPath, poller.sshEnv, poller.logger)
		if err != nil || repo == nil {
			return nil, err
		}
		return repo, nil
	}
	if configMirrorDataPath != "" {
		poller.findConfigMirror = func(url string) (repoOpener, error) {
			repo, err := mirror.Find(url, configMirrorDataPath, poller.sshEnv, poller.logger)
			if err != nil || repo == nil {
				return nil, err
			}
			return repo, nil
		}
	}
	return poller
}

// Returns entries for all repos with poll or hybrid trigger mode.
// Register the returned entries with a repowatcher.Watcher to activate polling.
func (poller *Poller) Subscriptions() []repowatcher.Subscription {
	var subs []repowatcher.Subscription
	for i := range poller.deployConfig.Repos {
		repo := poller.deployConfig.Repos[i]
		if repo.TriggerMode != config.TriggerModePoll && repo.TriggerMode != config.TriggerModeHybrid {
			continue
		}
		interval := repo.PollInterval
		if interval <= 0 {
			interval = repowatcher.DefaultPollInterval
		}
		subs = append(subs, repowatcher.Subscription{
			URL:      repo.URL,
			Branch:   repo.Branch,
			Interval: interval,
			Handler:  poller.makeHandler(repo),
		})
	}
	return subs
}

func (poller *Poller) makeHandler(repo config.RepoConfig) func(context.Context, string) {
	return func(ctx context.Context, sha string) {
		poller.HandleSHA(ctx, repo, sha)
	}
}

// Compares against stored state, computes a file diff when a local mirror is available,
// and dispatches a deploy event when a new commit is detected.
func (poller *Poller) HandleSHA(ctx context.Context, repo config.RepoConfig, sha string) {
	now := time.Now().UTC()

	repoState, err := poller.store.GetRepoState(repo.Name, repo.Branch)
	if err != nil {
		poller.logger.Warn("failed to read repo state", "repo", repo.Name, "err", err)
		return
	}
	if repoState == nil {
		repoState = &state.RepoState{
			Repo:   repo.Name,
			Branch: repo.Branch,
		}
	}

	repoState.LastSeenSha = sha
	repoState.LastPolledAt = &now

	if sha == repoState.LastDeployedSha {
		if err := poller.store.SaveRepoState(repoState); err != nil {
			poller.logger.Warn("failed to save repo state", "repo", repo.Name, "err", err)
		}
		poller.logger.Debug("no change", "repo", repo.Name, "sha", utils.ShortSha(sha))
		return
	}

	poller.logger.Info("new commit detected",
		"repo", repo.Name,
		"branch", repo.Branch,
		"prev", utils.ShortSha(repoState.LastDeployedSha),
		"new", utils.ShortSha(sha),
	)

	if repoState.LastDeployedSha != "" && poller.isStalePoll(ctx, repo.URL, sha, repoState.LastDeployedSha) {
		poller.logger.Warn("stale poll: SHA is an ancestor of last deployed, skipping",
			"repo", repo.Name, "sha", utils.ShortSha(sha), "deployed", utils.ShortSha(repoState.LastDeployedSha))
		if err := poller.store.SaveRepoState(repoState); err != nil {
			poller.logger.Warn("failed to save repo state", "repo", repo.Name, "err", err)
		}
		return
	}

	var changedFiles []string
	if localMirror, findErr := poller.findRepo(repo.URL); findErr == nil && localMirror != nil {
		if repoState.LastDeployedSha != "" && localMirror.HasCommit(ctx, repoState.LastDeployedSha) {
			if !localMirror.HasCommit(ctx, sha) {
				if err := localMirror.EnsureCommit(ctx, sha); err != nil {
					poller.logger.Warn("could not fetch new commit for diff, deploying unconditionally",
						"repo", repo.Name, "err", err)
				}
			}
			if localMirror.HasCommit(ctx, sha) {
				if files, dErr := localMirror.DiffFiles(ctx, repoState.LastDeployedSha, sha); dErr == nil {
					changedFiles = files
				} else {
					poller.logger.Warn("diff failed, deploying unconditionally", "repo", repo.Name, "err", dErr)
				}
			}
		}
	}

	if poller.reloadConfigRepo != nil {
		if err := poller.reloadConfigRepo(ctx, repo.Name); err != nil {
			poller.logger.Warn("config repo reload failed, deferring deploy until config is valid",
				"repo", repo.Name, "err", err)
			if err := poller.store.SaveRepoState(repoState); err != nil {
				poller.logger.Warn("failed to save repo state", "repo", repo.Name, "err", err)
			}
			return
		}
	}

	repoState.LastDeployedSha = sha
	repoState.TriggerSource = string(planner.TriggerPoll)
	if err := poller.store.SaveRepoState(repoState); err != nil {
		poller.logger.Warn("failed to save repo state before dispatch", "repo", repo.Name, "err", err)
	}

	poller.onDeploy(ctx, planner.RepoEvent{
		Source:       planner.TriggerPoll,
		RepoName:     repo.Name,
		Branch:       repo.Branch,
		CommitSha:    sha,
		ChangedFiles: changedFiles,
	})
}

// Checks both the deploy mirror and the config mirror (when present)
// to determine if sha is an ancestor of deployedSha, meaning the remote
// returned an older commit than what was already deployed.
func (poller *Poller) isStalePoll(ctx context.Context, repoURL, sha, deployedSha string) bool {
	finders := []func(string) (repoOpener, error){poller.findRepo}
	if poller.findConfigMirror != nil {
		finders = append(finders, poller.findConfigMirror)
	}
	for _, find := range finders {
		localMirror, err := find(repoURL)
		if err != nil || localMirror == nil {
			continue
		}
		if localMirror.HasCommit(ctx, sha) && localMirror.HasCommit(ctx, deployedSha) {
			return localMirror.IsAncestor(ctx, sha, deployedSha)
		}
	}
	return false
}
