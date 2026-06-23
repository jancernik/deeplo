// Package planner maps a Git push event to the set of project-host pairs that should be deployed.
package planner

import (
	"path/filepath"
	"strings"

	"github.com/jancernik/deeplo/internal/config"
)

type TriggerSource string

const (
	TriggerWebhook                TriggerSource = "webhook"
	TriggerPoll                   TriggerSource = "poll"
	TriggerReconcileAddition      TriggerSource = "addition"
	TriggerReconcileProjectChange TriggerSource = "change"
	TriggerResume                 TriggerSource = "resume"
	TriggerRedeploy               TriggerSource = "redeploy"
)

// Describes a Git push for a specific repo, received via webhook or detected via polling.
type RepoEvent struct {
	Source        TriggerSource
	DeliveryID    string
	RepoName      string
	Branch        string
	CommitSha     string
	ChangedFiles  []string
	ForcedTargets []DeployTarget
	Redeploy      bool
}

type DeployTarget struct {
	Project config.Project
	Host    config.Host
	Repo    config.RepoConfig
}

func AllTargets(deployConfig *config.Config) []DeployTarget {
	repoByName := deployConfig.RepoIndex()
	hostByName := deployConfig.HostIndex()

	var targets []DeployTarget
	for _, project := range deployConfig.Projects {
		repo, ok := repoByName[project.Repo]
		if !ok {
			continue
		}
		for _, hostName := range project.Targets {
			host, ok := hostByName[hostName]
			if !ok {
				continue
			}
			targets = append(targets, DeployTarget{Project: project, Host: host, Repo: repo})
		}
	}
	return targets
}

// Returns the DeployTargets that should be triggered for event.
func Plan(deployConfig *config.Config, event RepoEvent) []DeployTarget {
	if len(event.ForcedTargets) > 0 {
		return event.ForcedTargets
	}

	repoByName := deployConfig.RepoIndex()
	hostByName := deployConfig.HostIndex()

	var targets []DeployTarget
	for _, project := range deployConfig.Projects {
		if project.Repo != event.RepoName {
			continue
		}
		watchPaths := project.WatchPaths
		if len(watchPaths) == 0 && project.RepoSubdir != "" {
			watchPaths = []string{project.RepoSubdir + "/**"}
		}
		if !filesMatchProject(watchPaths, event.ChangedFiles) {
			continue
		}
		repo, ok := repoByName[project.Repo]
		if !ok {
			continue
		}
		for _, hostName := range project.Targets {
			host, ok := hostByName[hostName]
			if !ok {
				continue
			}
			targets = append(targets, DeployTarget{Project: project, Host: host, Repo: repo})
		}
	}
	return targets
}

func filesMatchProject(watchPaths, changedFiles []string) bool {
	if len(watchPaths) == 0 {
		return true
	}
	if changedFiles == nil {
		return true // unknown diff, deploy unconditionally
	}
	for _, changedFile := range changedFiles {
		for _, pattern := range watchPaths {
			if MatchPath(pattern, changedFile) {
				return true
			}
		}
	}
	return false
}

func MatchPath(pattern, filePath string) bool {
	patParts := strings.Split(pattern, "/")
	fileParts := strings.Split(filePath, "/")
	return matchParts(patParts, fileParts)
}

func matchParts(patternParts, fileParts []string) bool {
	for {
		if len(patternParts) == 0 {
			return len(fileParts) == 0
		}

		if patternParts[0] == "**" {
			if matchParts(patternParts[1:], fileParts) {
				return true
			}
			if len(fileParts) == 0 {
				return false
			}
			fileParts = fileParts[1:]
			continue
		}

		if len(fileParts) == 0 {
			return false
		}
		ok, err := filepath.Match(patternParts[0], fileParts[0])
		if err != nil || !ok {
			return false
		}
		patternParts = patternParts[1:]
		fileParts = fileParts[1:]
	}
}
