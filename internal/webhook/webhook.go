// Package webhook defines the normalized push event type shared between
// webhook provider implementations and the engine.
package webhook

type PushEvent struct {
	DeliveryID   string
	RepoFullName string
	Branch       string
	CommitSha    string
	ChangedFiles []string
}
