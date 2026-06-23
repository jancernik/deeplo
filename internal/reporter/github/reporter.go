package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jancernik/deeplo/internal/reporter"
	"github.com/jancernik/deeplo/internal/utils"
)

const (
	defaultAPIBase = "https://api.github.com"
	apiVersion     = "2022-11-28"
	maxDescription = 140
)

type Config struct {
	TokenFile       string
	Environment     string
	EnvironmentHost bool
}

type Reporter struct {
	token           string
	apiBase         string
	environmentBase string
	environmentHost bool
	client          *http.Client
	logger          *slog.Logger
}

func New(config Config, logger *slog.Logger) (reporter.Reporter, error) {
	if config.TokenFile == "" {
		return &Reporter{}, nil
	}
	data, err := os.ReadFile(config.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("read github token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return nil, fmt.Errorf("github token file %q is empty", config.TokenFile)
	}
	return newReporter(token, defaultAPIBase, config.Environment, config.EnvironmentHost, nil, logger), nil
}

func newReporter(token, apiBase, environmentBase string, environmentHost bool, client *http.Client, logger *slog.Logger) *Reporter {
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Reporter{
		token:           token,
		apiBase:         apiBase,
		environmentBase: environmentBase,
		environmentHost: environmentHost,
		client:          client,
		logger:          logger.With("component", "github"),
	}
}

func (githubReporter *Reporter) Enabled() bool {
	return githubReporter != nil && githubReporter.token != ""
}

func (githubReporter *Reporter) DeployStarted(ctx context.Context, info reporter.DeployInfo, logURL string) string {
	if !githubReporter.Enabled() {
		return ""
	}

	deploymentContext, err := githubReporter.deployContext(info)
	if err != nil {
		githubReporter.logger.Warn("cannot build deploy context, skipping", "url", info.RepoURL, "err", err)
		return ""
	}

	deploymentID, err := githubReporter.createDeployment(ctx, deploymentContext)
	if err != nil {
		githubReporter.logger.Warn("create deployment failed",
			"owner", deploymentContext.owner, "repo", deploymentContext.repo, "sha", utils.ShortSha(deploymentContext.sha), "err", err)
		return ""
	}
	githubReporter.logger.Debug("deployment created",
		"owner", deploymentContext.owner, "repo", deploymentContext.repo, "deployment_id", deploymentID)

	if err := githubReporter.createDeploymentStatus(ctx, deploymentContext.owner, deploymentContext.repo, deploymentID, "in_progress", "Deployment started", logURL); err != nil {
		githubReporter.logger.Warn("deployment status in_progress failed", "err", err)
	}
	if err := githubReporter.createCommitStatus(ctx, deploymentContext, "pending", "Deployment started", logURL); err != nil {
		githubReporter.logger.Warn("commit status pending failed", "err", err)
	}

	return strconv.FormatInt(deploymentID, 10)
}

func (githubReporter *Reporter) DeploySucceeded(ctx context.Context, info reporter.DeployInfo, token, summary, logURL string) error {
	if !githubReporter.Enabled() {
		return nil
	}
	return githubReporter.reportFinish(ctx, info, token, true, summary, logURL)
}

func (githubReporter *Reporter) DeployFailed(ctx context.Context, info reporter.DeployInfo, token, summary, logURL string) error {
	if !githubReporter.Enabled() {
		return nil
	}
	return githubReporter.reportFinish(ctx, info, token, false, summary, logURL)
}

func (githubReporter *Reporter) reportFinish(ctx context.Context, info reporter.DeployInfo, token string, success bool, summary, logURL string) error {
	deploymentContext, err := githubReporter.deployContext(info)
	if err != nil {
		githubReporter.logger.Warn("cannot build deploy context, skipping", "url", info.RepoURL, "err", err)
		return nil
	}

	ghState := "failure"
	commitState := "failure"
	if success {
		ghState = "success"
		commitState = "success"
	}
	summary = truncate(summary, maxDescription)

	var firstErr error
	save := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if token != "" {
		deploymentID, parseErr := strconv.ParseInt(token, 10, 64)
		if parseErr != nil {
			githubReporter.logger.Warn("invalid deployment token, skipping deployment status update", "token", token, "err", parseErr)
		} else {
			err := githubReporter.createDeploymentStatus(ctx, deploymentContext.owner, deploymentContext.repo, deploymentID, ghState, summary, logURL)
			if err != nil {
				githubReporter.logger.Warn("deployment status update failed",
					"state", ghState, "deployment_id", deploymentID, "err", err)
			}
			save(err)
		}
	}

	err = githubReporter.createCommitStatus(ctx, deploymentContext, commitState, summary, logURL)
	if err != nil {
		githubReporter.logger.Warn("commit status update failed",
			"state", commitState, "sha", utils.ShortSha(deploymentContext.sha), "err", err)
	}
	save(err)

	return firstErr
}

func ParseOwnerRepo(repoURL string) (owner, repo string, err error) {
	normalizedURL := strings.TrimSuffix(strings.TrimSpace(repoURL), ".git")

	if _, after, ok := strings.Cut(normalizedURL, "github.com:"); ok {
		parts := strings.SplitN(after, "/", 3)
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], nil
		}
	}

	if _, after, ok := strings.Cut(normalizedURL, "github.com/"); ok {
		parts := strings.SplitN(after, "/", 3)
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], nil
		}
	}

	return "", "", fmt.Errorf("cannot parse GitHub owner/repo from URL %q", repoURL)
}

type deployContext struct {
	owner         string
	repo          string
	sha           string
	environment   string
	commitContext string
}

func (githubReporter *Reporter) deployContext(info reporter.DeployInfo) (deployContext, error) {
	owner, repo, err := ParseOwnerRepo(info.RepoURL)
	if err != nil {
		return deployContext{}, err
	}

	var environment string
	if githubReporter.environmentHost {
		environment = info.HostName + "/" + info.ProjectName
	} else {
		environment = info.ProjectName
	}
	if githubReporter.environmentBase != "" {
		environment = githubReporter.environmentBase + "/" + environment
	}

	return deployContext{
		owner:         owner,
		repo:          repo,
		sha:           info.CommitSHA,
		environment:   environment,
		commitContext: "deeplo/" + environment,
	}, nil
}
