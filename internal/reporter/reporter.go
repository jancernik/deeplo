// Package reporter defines the interface that deploy reporters must satisfy.
// A reporter notifies an external system of deploy lifecycle events.
package reporter

import "context"

type DeployInfo struct {
	RepoURL     string
	CommitSHA   string
	ProjectName string
	HostName    string
}

// Reports deploy lifecycle events to an external system.
// All methods must be safe to call on a disabled reporter (all become noops).
type Reporter interface {
	// Reports whether this reporter is active.
	Enabled() bool

	// Signals that a deploy has begun.
	// Returns a token that must be passed to DeploySucceeded or DeployFailed.
	// Returns "" when reporting is disabled or the call fails.
	DeployStarted(ctx context.Context, info DeployInfo, logURL string) string

	// Signals that a deploy completed successfully.
	// token is the value returned by DeployStarted for this deploy.
	DeploySucceeded(ctx context.Context, info DeployInfo, token string, summary, logURL string) error

	// Signals that a deploy failed.
	// token is the value returned by DeployStarted for this deploy.
	DeployFailed(ctx context.Context, info DeployInfo, token string, summary, logURL string) error
}

// Returns a Reporter that does nothing. Enabled() always returns false.
func Noop() Reporter { return noopReporter{} }

type noopReporter struct{}

func (noopReporter) Enabled() bool { return false }
func (noopReporter) DeployStarted(_ context.Context, _ DeployInfo, _ string) string {
	return ""
}
func (noopReporter) DeploySucceeded(_ context.Context, _ DeployInfo, _, _, _ string) error {
	return nil
}
func (noopReporter) DeployFailed(_ context.Context, _ DeployInfo, _, _, _ string) error {
	return nil
}
