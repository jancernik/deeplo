// Package client provides a Go client for the deeplo admin API.
// It connects over a Unix socket and returns typed responses.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jancernik/deeplo/internal/api"
)

type Client struct {
	httpClient *http.Client
	socket     string
}

func New(socketPath string) *Client {
	return &Client{
		socket: socketPath,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			},
			Timeout: 60 * time.Second,
		},
	}
}

func (client *Client) Health(ctx context.Context) (*api.HealthResponse, error) {
	var resp api.HealthResponse
	if err := client.get(ctx, "/api/v1/health", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (client *Client) Deployments(ctx context.Context) (*api.DeploymentsResponse, error) {
	var resp api.DeploymentsResponse
	if err := client.get(ctx, "/api/v1/deployments", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (client *Client) Runs(ctx context.Context, project, host string, limit int) (*api.RunsResponse, error) {
	u := "/api/v1/runs"
	var params []string
	if project != "" {
		params = append(params, "project="+project)
	}
	if host != "" {
		params = append(params, "host="+host)
	}
	if limit > 0 {
		params = append(params, fmt.Sprintf("limit=%d", limit))
	}
	if len(params) > 0 {
		u += "?" + strings.Join(params, "&")
	}
	var resp api.RunsResponse
	if err := client.get(ctx, u, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (client *Client) RunLog(ctx context.Context, id string) (string, error) {
	resp, err := client.do(ctx, http.MethodGet, "/api/v1/runs/"+id+"/log")
	if err != nil {
		return "", err
	}
	defer client.closeBody(resp)
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("run log not found: %s", id)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server error: %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read log: %w", err)
	}
	return string(data), nil
}

func (client *Client) Refresh(ctx context.Context) (*api.RefreshResponse, error) {
	var resp api.RefreshResponse
	if err := client.post(ctx, "/api/v1/refresh", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (client *Client) Probe(ctx context.Context) (*api.ProbeResponse, error) {
	var resp api.ProbeResponse
	if err := client.post(ctx, "/api/v1/probe", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (client *Client) Reload(ctx context.Context) (*api.ReloadResponse, error) {
	var resp api.ReloadResponse
	if err := client.post(ctx, "/api/v1/reload", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (client *Client) Deploy(ctx context.Context, project, host string) (*api.DeployResponse, error) {
	path := "/api/v1/deploy?project=" + project
	if host != "" {
		path += "&host=" + host
	}
	var resp api.DeployResponse
	if err := client.post(ctx, path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Builds and sends a request to path, returns the response.
func (client *Client) do(ctx context.Context, method, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://deeplo"+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.httpClient.Do(req)
	if err != nil {
		return nil, client.dialErr(err)
	}
	return resp, nil
}

// Closes the response body, logs any error.
func (client *Client) closeBody(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		slog.Warn("close response body", "err", err)
	}
}

func (client *Client) get(ctx context.Context, path string, out any) error {
	resp, err := client.do(ctx, http.MethodGet, path)
	if err != nil {
		return err
	}
	defer client.closeBody(resp)
	return client.decode(resp, out)
}

func (client *Client) post(ctx context.Context, path string, out any) error {
	resp, err := client.do(ctx, http.MethodPost, path)
	if err != nil {
		return err
	}
	defer client.closeBody(resp)
	return client.decode(resp, out)
}

func (client *Client) decode(resp *http.Response, out any) error {
	if resp.StatusCode >= 400 {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e) //nolint:errcheck
		if e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("server returned %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (client *Client) dialErr(err error) error {
	return fmt.Errorf("daemon not reachable at %s: %w\n  Is the daemon running? Run: deeplo health", client.socket, err)
}
