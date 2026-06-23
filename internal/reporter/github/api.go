package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type createDeploymentBody struct {
	Ref              string   `json:"ref"`
	Environment      string   `json:"environment"`
	Description      string   `json:"description,omitempty"`
	AutoMerge        bool     `json:"auto_merge"`
	RequiredContexts []string `json:"required_contexts"`
}

type createDeploymentResponse struct {
	ID int64 `json:"id"`
}

func (githubReporter *Reporter) createDeployment(ctx context.Context, deploymentContext deployContext) (int64, error) {
	body := createDeploymentBody{
		Ref:              deploymentContext.sha,
		Environment:      deploymentContext.environment,
		Description:      "Deployment via deeplo",
		AutoMerge:        false,
		RequiredContexts: []string{},
	}
	var resp createDeploymentResponse
	if err := githubReporter.apiPost(ctx, fmt.Sprintf("/repos/%s/%s/deployments", deploymentContext.owner, deploymentContext.repo), body, &resp); err != nil {
		return 0, err
	}
	return resp.ID, nil
}

type createDeploymentStatusBody struct {
	State       string `json:"state"`
	Description string `json:"description,omitempty"`
	LogURL      string `json:"log_url,omitempty"`
}

func (githubReporter *Reporter) createDeploymentStatus(ctx context.Context, owner, repo string, deploymentID int64, state, description, logURL string) error {
	body := createDeploymentStatusBody{
		State:       state,
		Description: truncate(description, maxDescription),
		LogURL:      logURL,
	}
	path := fmt.Sprintf("/repos/%s/%s/deployments/%d/statuses", owner, repo, deploymentID)
	return githubReporter.apiPost(ctx, path, body, nil)
}

type createCommitStatusBody struct {
	State       string `json:"state"`
	Context     string `json:"context,omitempty"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
}

func (githubReporter *Reporter) createCommitStatus(ctx context.Context, deploymentContext deployContext, state, description, targetURL string) error {
	body := createCommitStatusBody{
		State:       state,
		Context:     deploymentContext.commitContext,
		Description: truncate(description, maxDescription),
		TargetURL:   targetURL,
	}
	return githubReporter.apiPost(ctx, fmt.Sprintf("/repos/%s/%s/statuses/%s", deploymentContext.owner, deploymentContext.repo, deploymentContext.sha), body, nil)
}

func (githubReporter *Reporter) apiPost(ctx context.Context, path string, body any, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubReporter.apiBase+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+githubReporter.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := githubReporter.client.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			githubReporter.logger.Warn("close response body", "err", err)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("github API %s → %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func truncate(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes-1]) + "…"
}
