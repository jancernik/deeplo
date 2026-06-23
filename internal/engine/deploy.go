package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/compose"
	"github.com/jancernik/deeplo/internal/mirror"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/reporter"
	"github.com/jancernik/deeplo/internal/runlog"
	"github.com/jancernik/deeplo/internal/ssh"
	"github.com/jancernik/deeplo/internal/state"
	"github.com/jancernik/deeplo/internal/utils"
)

func ShouldSkipDeploy(store *state.FileStore, project, host, sha string) bool {
	latest, err := store.GetLatestDeployment(project, host)
	if err != nil || latest == nil {
		return false
	}
	if latest.CommitSha != sha {
		return false
	}
	return latest.Status == state.StatusRunning ||
		latest.Status == state.StatusSuccess
}

// Performs the full deploy for one project-host target.
func DeployTarget(
	ctx context.Context,
	env *bootstrap.Config,
	target planner.DeployTarget,
	event planner.RepoEvent,
	sshEnv []string,
	dialer ssh.Dialer,
	store *state.FileStore,
	deployReporter reporter.Reporter,
	logger *slog.Logger,
) error {
	project := target.Project
	host := target.Host
	repo := target.Repo

	deployID := state.NewID()
	log := logger.With("project", project.Name, "host", host.Name, "id", deployID)
	startedAt := time.Now().UTC()
	deployment := &state.Deployment{
		ID:            deployID,
		Project:       project.Name,
		Host:          host.Name,
		CommitSha:     event.CommitSha,
		Branch:        event.Branch,
		TriggerSource: string(event.Source),
		Status:        state.StatusRunning,
		StartedAt:     startedAt,
	}
	if err := store.SaveDeployment(deployment); err != nil {
		log.Warn("failed to save deployment start state", "err", err)
	}

	runsDir := filepath.Join(env.DataPath, "runs")
	runLog, rlErr := runlog.Open(runsDir, deployID)
	if rlErr != nil {
		log.Warn("failed to create run log, deploy proceeds", "err", rlErr)
	}
	defer func() {
		if err := runLog.Close(); err != nil {
			log.Warn("close run log", "err", err)
		}
	}()

	runLog.Println("Run:      " + deployID)
	runLog.Println("Project:  " + project.Name)
	runLog.Println("Host:     " + host.Name + " (" + host.Address + ")")
	runLog.Println("Commit:   " + event.CommitSha)
	runLog.Println("Branch:   " + event.Branch)
	runLog.Println("Source:   " + string(event.Source))
	runLog.Println("Started:  " + startedAt.Format(time.RFC3339))
	runLog.Println("")

	var logURL string
	if env.PublicURL != "" {
		logURL = env.PublicURL + "/runs/" + deployID + "/logs"
	}

	reporterInfo := reporter.DeployInfo{
		RepoURL:     repo.URL,
		CommitSHA:   event.CommitSha,
		ProjectName: project.Name,
		HostName:    host.Name,
	}
	reportToken := deployReporter.DeployStarted(ctx, reporterInfo, logURL)
	deployment.ReportToken = reportToken

	deployErr := runDeploy(ctx, env, target, event, sshEnv, dialer, runLog, log, logger)

	elapsed := time.Since(startedAt).Round(time.Millisecond)

	summary := ConciseSummary(deployErr, host.Name)
	if deployErr != nil {
		runLog.Println("")
		runLog.Logf("Deploy FAILED in %s - %s", elapsed, summary)
	} else {
		runLog.Logf("Deploy succeeded in %s", elapsed)
	}
	if deployReporter.Enabled() {
		var reportErr error
		if deployErr != nil {
			reportErr = deployReporter.DeployFailed(ctx, reporterInfo, reportToken, summary, logURL)
			deployment.ReportStatus = "failure"
		} else {
			reportErr = deployReporter.DeploySucceeded(ctx, reporterInfo, reportToken, summary, logURL)
			deployment.ReportStatus = "success"
		}
		if reportErr != nil {
			deployment.ReportError = reportErr.Error()
			log.Warn("reporting failed", "err", reportErr)
		}
	}

	now := time.Now().UTC()
	deployment.CompletedAt = &now
	if deployErr != nil {
		deployment.Status = state.StatusFailed
		deployment.Error = deployErr.Error()
	} else {
		deployment.Status = state.StatusSuccess
	}
	if err := store.SaveDeployment(deployment); err != nil {
		log.Warn("failed to save deployment final state", "err", err)
	}

	return deployErr
}

// Performs the core mechanics for a single deploy target.
// git fetch - bundle extraction - SSH dial - docker compose up.
func runDeploy(
	ctx context.Context,
	env *bootstrap.Config,
	target planner.DeployTarget,
	event planner.RepoEvent,
	sshEnv []string,
	dialer ssh.Dialer,
	runLog *runlog.RunLog,
	connLog *slog.Logger,
	logger *slog.Logger,
) error {
	project := target.Project
	host := target.Host
	repo := target.Repo

	runLog.Logf("Fetching repository %s", repo.URL)
	gitRepo, err := mirror.Open(ctx, repo.URL, env.DataPath, sshEnv, logger)
	if err != nil {
		runLog.Logf("FAILED: %v", err)
		return fmt.Errorf("open repo: %w", err)
	}
	if err := gitRepo.EnsureCommit(ctx, event.CommitSha); err != nil {
		runLog.Logf("FAILED: %v", err)
		return fmt.Errorf("ensure commit: %w", err)
	}
	runLog.Logf("Commit %s is available", utils.ShortSha(event.CommitSha))

	tmpDir, err := os.MkdirTemp("", "deeplo-bundle-*")
	if err != nil {
		runLog.Logf("FAILED: creating temp dir: %v", err)
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	runLog.Logf("Extracting bundle from %s", project.RepoSubdir)
	bundle, err := buildBundle(ctx, gitRepo, event.CommitSha, project, tmpDir)
	if err != nil {
		runLog.Logf("FAILED: %v", err)
		return fmt.Errorf("build bundle: %w", err)
	}

	sshUser := host.EffectiveUser(env.SSHUser)
	sshPort := host.EffectivePort(env.SSHPort)
	runLog.Logf("Connecting to %s (%s@%s:%d)", host.Name, sshUser, host.Address, sshPort)
	conn, err := dialer.Dial(ctx, ssh.DialConfig{
		Address:        host.Address,
		Port:           sshPort,
		User:           sshUser,
		PrivateKeyFile: env.SSHPrivateKeyFile,
		KnownHostsFile: env.SSHKnownHosts,
		HostKeyPolicy:  env.SSHHostKeyPolicy,
	})
	if err != nil {
		runLog.Logf("FAILED: %v", err)
		return fmt.Errorf("dial %s (%s): %w", host.Name, host.Address, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			connLog.Warn("close SSH connection", "err", err)
		}
	}()

	remoteDir := path.Join(host.DeployDir, project.DeploySubdir)
	runLog.Logf("Running docker compose up in %s", remoteDir)
	executor := compose.NewExecutor(conn, remoteDir, project.Name, logger)
	result, err := executor.Deploy(ctx, bundle, compose.DeployOptions{
		ComposeFiles: project.ComposeFiles,
		PersistFiles: project.PersistFiles,
	})
	if result != nil && result.ComposeOutput != "" {
		runLog.Println("")
		runLog.Println("--- docker compose up ---")
		runLog.Println(result.ComposeOutput)
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "runtime check:") {
			runLog.Println("")
			runLog.Logf("FAILED: runtime verification")
			if result != nil && len(result.Services) > 0 {
				runLog.Println("")
				runLog.Println("--- Service states ---")
				for _, svc := range result.Services {
					runLog.Println("  " + svc.Service + ": " + svc.State)
				}
			}
		} else {
			runLog.Logf("FAILED: docker compose up")
		}
		runLog.Println("")
		runLog.Println("--- Error detail ---")
		runLog.Println(err.Error())
		return err
	}
	return nil
}

// Returns a short status description for a deploy outcome.
func ConciseSummary(err error, host string) string {
	if err == nil {
		return "Deployed successfully"
	}
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "open repo:"),
		strings.HasPrefix(msg, "ensure commit:"):
		return "Git fetch failed on " + host
	case strings.HasPrefix(msg, "build bundle:"):
		return "Bundle error on " + host
	case strings.HasPrefix(msg, "mktemp:"):
		return "Storage error on " + host
	case strings.HasPrefix(msg, "dial "):
		return "SSH connection failed for " + host
	case strings.HasPrefix(msg, "preflight:"):
		return "Preflight failed on " + host
	case strings.HasPrefix(msg, "runtime check:"):
		return "Runtime verification failed on " + host
	default:
		return "Deploy failed on " + host
	}
}
