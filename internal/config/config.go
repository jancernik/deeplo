// Package config defines the YAML deploy configuration.
package config

import "time"

type TriggerMode string

const (
	TriggerModeWebhook TriggerMode = "webhook"
	TriggerModePoll    TriggerMode = "poll"
	TriggerModeHybrid  TriggerMode = "hybrid"
)

type Config struct {
	Hosts    []Host       `yaml:"hosts"`
	Repos    []RepoConfig `yaml:"repos"`
	Projects []Project    `yaml:"projects"`
}

type RepoConfig struct {
	Name         string        `yaml:"name"`
	URL          string        `yaml:"url"`
	Branch       string        `yaml:"branch"`
	TriggerMode  TriggerMode   `yaml:"trigger_mode"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

type Host struct {
	Name      string `yaml:"name"`
	Address   string `yaml:"address"`
	DeployDir string `yaml:"deploy_dir"`
	User      string `yaml:"user"`
	Port      int    `yaml:"port"`
}

func (host Host) EffectiveUser(defaultUser string) string {
	if host.User != "" {
		return host.User
	}
	return defaultUser
}

func (host Host) EffectivePort(defaultPort int) int {
	if host.Port != 0 {
		return host.Port
	}
	return defaultPort
}

func (config *Config) RepoIndex() map[string]RepoConfig {
	index := make(map[string]RepoConfig, len(config.Repos))
	for _, repo := range config.Repos {
		index[repo.Name] = repo
	}
	return index
}

func (config *Config) HostIndex() map[string]Host {
	index := make(map[string]Host, len(config.Hosts))
	for _, host := range config.Hosts {
		index[host.Name] = host
	}
	return index
}

func (config *Config) ProjectIndex() map[string]Project {
	index := make(map[string]Project, len(config.Projects))
	for _, project := range config.Projects {
		index[project.Name] = project
	}
	return index
}

type Project struct {
	Name         string   `yaml:"name"`
	Repo         string   `yaml:"repo"`
	RepoSubdir   string   `yaml:"repo_subdir"`
	ComposeFiles []string `yaml:"compose_files"`
	Targets      []string `yaml:"targets"`
	WatchPaths   []string `yaml:"watch_paths"`
	DeploySubdir string   `yaml:"deploy_subdir"`
	ExtraFiles   []string `yaml:"extra_files"`
	PersistFiles []string `yaml:"persist_files"`
}
