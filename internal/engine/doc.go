// Package engine connects push events to Docker Compose deployments.
// It routes events to project-host targets, transfers bundles over
// SSH, runs docker compose up, and reconciles config changes.
package engine
