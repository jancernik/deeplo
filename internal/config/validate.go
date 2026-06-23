package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

type Severity int

const (
	SeverityError Severity = iota
	SeverityWarning
)

type ValidationIssue struct {
	Field    string
	Message  string
	Severity Severity
}

func (config *Config) Validate() []ValidationIssue {
	var issues []ValidationIssue

	add := func(field, format string, args ...any) {
		issues = append(issues, ValidationIssue{Field: field, Message: fmt.Sprintf(format, args...), Severity: SeverityError})
	}
	addWarning := func(field, format string, args ...any) {
		issues = append(issues, ValidationIssue{Field: field, Message: fmt.Sprintf(format, args...), Severity: SeverityWarning})
	}

	// Hosts
	if len(config.Hosts) == 0 {
		add("hosts", "at least one host is required")
	}
	seenHosts := make(map[string]bool)
	for i, host := range config.Hosts {
		prefix := fmt.Sprintf("hosts[%d]", i)
		if host.Name == "" {
			add(prefix+".name", "name is required")
		} else if seenHosts[host.Name] {
			add(prefix+".name", "duplicate host name %q", host.Name)
		} else {
			seenHosts[host.Name] = true
		}
		if host.Address == "" {
			add(prefix+".address", "address is required")
		}
		if host.DeployDir == "" {
			add(prefix+".deploy_dir", "deploy_dir is required")
		}
		if host.Port != 0 && (host.Port < 1 || host.Port > 65535) {
			add(prefix+".port", "must be 1–65535 when set, got %d", host.Port)
		}
	}

	// Repos
	if len(config.Repos) == 0 {
		add("repos", "at least one repo is required")
	}
	seenRepos := make(map[string]bool)
	for i, repo := range config.Repos {
		prefix := fmt.Sprintf("repos[%d]", i)
		if repo.Name == "" {
			add(prefix+".name", "name is required")
		} else if seenRepos[repo.Name] {
			add(prefix+".name", "duplicate repo name %q", repo.Name)
		} else {
			seenRepos[repo.Name] = true
		}
		if repo.URL == "" {
			add(prefix+".url", "url is required")
		}
		if repo.Branch == "" {
			add(prefix+".branch", "branch is required")
		}
		switch repo.TriggerMode {
		case TriggerModeWebhook, TriggerModePoll, TriggerModeHybrid, "":
		default:
			add(prefix+".trigger_mode", "unknown trigger mode %q, must be webhook, poll, or hybrid", repo.TriggerMode)
		}
	}

	// Projects
	if len(config.Projects) == 0 {
		addWarning("projects", "no projects configured; the daemon will deploy nothing and tear down existing deployments")
	}
	seenProjects := make(map[string]bool)
	for i, project := range config.Projects {
		prefix := fmt.Sprintf("projects[%d]", i)
		if project.Name == "" {
			add(prefix+".name", "name is required")
		} else if seenProjects[project.Name] {
			add(prefix+".name", "duplicate project name %q", project.Name)
		} else {
			seenProjects[project.Name] = true
		}
		if project.Repo == "" {
			add(prefix+".repo", "repo is required")
		} else if !seenRepos[project.Repo] {
			add(prefix+".repo", "repo %q does not reference a known repo", project.Repo)
		}
		if project.RepoSubdir == "" {
			add(prefix+".repo_subdir", "repo_subdir is required")
		}
		if len(project.ComposeFiles) == 0 {
			add(prefix+".compose_files", "at least one compose file is required")
		}
		for j, pattern := range project.WatchPaths {
			if err := validateWatchPath(pattern); err != nil {
				add(fmt.Sprintf("%s.watch_paths[%d]", prefix, j), "%v", err)
			}
		}
		for j, name := range project.PersistFiles {
			if err := validatePersistFile(name); err != nil {
				add(fmt.Sprintf("%s.persist_files[%d]", prefix, j), "%v", err)
			}
		}
		if len(project.Targets) == 0 {
			add(prefix+".targets", "at least one target host is required")
		}
		for j, target := range project.Targets {
			if !seenHosts[target] {
				add(fmt.Sprintf("%s.targets[%d]", prefix, j), "target %q does not reference a known host", target)
			}
		}
	}

	return issues
}

func (config *Config) BlockingIssues() []ValidationIssue {
	var blocking []ValidationIssue
	for _, issue := range config.Validate() {
		if issue.Severity == SeverityError {
			blocking = append(blocking, issue)
		}
	}
	return blocking
}

func validatePersistFile(name string) error {
	if name == "" {
		return fmt.Errorf("persist file name must not be empty")
	}
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("persist file name must be relative, got %q", name)
	}
	if slices.Contains(strings.Split(name, "/"), "..") {
		return fmt.Errorf("persist file name must not contain '..', got %q", name)
	}
	return nil
}

func validateWatchPath(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("watch path must not be empty")
	}
	for segment := range strings.SplitSeq(pattern, "/") {
		if segment == "**" {
			continue
		}
		if _, err := filepath.Match(segment, "x"); err != nil {
			return fmt.Errorf("invalid watch path pattern %q: %w", pattern, err)
		}
	}
	return nil
}
