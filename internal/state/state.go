// Package state persists deployment records and per-repo poll state to the local filesystem as JSON files.
package state

import "time"

type DeploymentStatus string

const (
	StatusPending DeploymentStatus = "pending"
	StatusRunning DeploymentStatus = "running"
	StatusSuccess DeploymentStatus = "success"
	StatusFailed  DeploymentStatus = "failed"
)

type Deployment struct {
	ID            string           `json:"id"`
	Project       string           `json:"project"`
	Host          string           `json:"host"`
	CommitSha     string           `json:"commit_sha"`
	Branch        string           `json:"branch"`
	Status        DeploymentStatus `json:"status"`
	TriggerSource string           `json:"trigger_source,omitempty"`
	StartedAt     time.Time        `json:"started_at"`
	CompletedAt   *time.Time       `json:"completed_at,omitempty"`
	Error         string           `json:"error,omitempty"`
	ReportToken   string           `json:"report_token,omitempty"`
	ReportStatus  string           `json:"report_status,omitempty"`
	ReportError   string           `json:"report_error,omitempty"`
}

type RepoState struct {
	Repo            string     `json:"repo"`
	Branch          string     `json:"branch"`
	LastSeenSha     string     `json:"last_seen_sha,omitempty"`
	LastDeployedSha string     `json:"last_deployed_sha,omitempty"`
	LastPolledAt    *time.Time `json:"last_polled_at,omitempty"`
	TriggerSource   string     `json:"trigger_source,omitempty"`
	LastDeliveryID  string     `json:"last_delivery_id,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
