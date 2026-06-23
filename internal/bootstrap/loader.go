package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/mirror"
	"github.com/jancernik/deeplo/internal/utils"
)

type ConfigResult struct {
	Config    *config.Config
	FromCache bool
	CommitSha string
	SourceURL string
}

type configMetadata struct {
	Branch    string    `json:"branch"`
	FetchedAt time.Time `json:"fetched_at"`
	CommitSha string    `json:"commit_sha"`
	SourceURL string    `json:"source_url"`
}

// SourceLocal: reads the local config file and validates it.
// SourceGit: fetches the config repo, reads the config file, validates it,
// updates the last-known-good cache, and falls back to cache on any error.
func LoadConfig(ctx context.Context, env *Config, logger *slog.Logger) (*ConfigResult, error) {
	switch env.Source {
	case SourceLocal:
		return loadLocalConfig(env.ConfigFile)
	case SourceGit:
		return loadGitConfig(ctx, env, logger)
	default:
		return nil, fmt.Errorf("unknown config source %q", env.Source)
	}
}

func loadLocalConfig(configFile string) (*ConfigResult, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read config file %q: %w", configFile, err)
	}
	configData, err := config.Parse(data)
	if err != nil {
		return nil, err
	}
	return &ConfigResult{Config: configData}, nil
}

func loadGitConfig(ctx context.Context, env *Config, logger *slog.Logger) (*ConfigResult, error) {
	cachedConfigDir := filepath.Join(env.DataPath, "config")
	if err := os.MkdirAll(cachedConfigDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create the cached config dir: %w", err)
	}
	cachedConfig := filepath.Join(cachedConfigDir, "current.yml")
	metadataFile := filepath.Join(cachedConfigDir, "metadata.json")
	sshEnv := mirror.SshEnv(env.SSHPrivateKeyFile, env.SSHKnownHosts, env.SSHHostKeyPolicy)

	data, sha, err := fetchConfigFromGit(ctx, env, cachedConfigDir, sshEnv, logger)
	if err != nil {
		return readCachedConfig(cachedConfig, env, logger)
	}

	configData, err := parseAndValidate(data)
	if err != nil {
		logger.Warn("fetched config is invalid, falling back to cache", "err", err)
		return readCachedConfig(cachedConfig, env, logger)
	}

	if err := writeCachedConfig(cachedConfig, metadataFile, data, env, sha); err != nil {
		logger.Warn("failed to update cache", "err", err)
	}

	return &ConfigResult{
		Config:    configData,
		FromCache: false,
		CommitSha: sha,
		SourceURL: env.ConfigRepoURL,
	}, nil
}

func fetchConfigFromGit(ctx context.Context, env *Config, cachedConfigDir string, sshEnv []string, logger *slog.Logger) ([]byte, string, error) {
	sha, err := mirror.RemoteSha(ctx, env.ConfigRepoURL, env.ConfigRepoBranch, sshEnv)
	if err != nil {
		logger.Warn("git ls-remote failed",
			"url", env.ConfigRepoURL, "branch", env.ConfigRepoBranch, "err", err)
		return nil, "", err
	}

	repo, err := mirror.Open(ctx, env.ConfigRepoURL, cachedConfigDir, sshEnv, logger)
	if err != nil {
		logger.Warn("failed to open or clone the repo", "err", err)
		return nil, "", err
	}

	if err := repo.EnsureCommit(ctx, sha); err != nil {
		logger.Warn("ensure commit failed", "sha", utils.ShortSha(sha), "err", err)
		return nil, "", err
	}

	data, err := repo.ReadFile(ctx, sha, env.ConfigRepoFile)
	if err != nil {
		logger.Warn("read config file failed",
			"file", env.ConfigRepoFile, "sha", utils.ShortSha(sha), "err", err)
		return nil, "", err
	}

	return data, sha, nil
}

func parseAndValidate(data []byte) (*config.Config, error) {
	configData, err := config.Parse(data)
	if err != nil {
		return nil, err
	}
	if errs := configData.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("invalid config %s: %s", errs[0].Field, errs[0].Message)
	}
	return configData, nil
}

func readCachedConfig(cachedConfig string, env *Config, logger *slog.Logger) (*ConfigResult, error) {
	data, err := os.ReadFile(cachedConfig)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config fetch failed and no cached config found, cannot start without a valid config")
		}
		return nil, fmt.Errorf("config fetch failed and cached config is unreadable: %w", err)
	}
	configData, err := config.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config fetch failed and cached config is unparseable: %w", err)
	}
	if errs := configData.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("config fetch failed and cached config is invalid: %s: %s", errs[0].Field, errs[0].Message)
	}
	logger.Warn("using last-known-good cached config", "path", cachedConfig)

	result := &ConfigResult{
		Config:    configData,
		FromCache: true,
		SourceURL: env.ConfigRepoURL,
	}

	metadataPath := filepath.Join(filepath.Dir(cachedConfig), "metadata.json")
	if cachedMetadata, err := os.ReadFile(metadataPath); err == nil {
		var metadata configMetadata
		if err := json.Unmarshal(cachedMetadata, &metadata); err == nil {
			result.CommitSha = metadata.CommitSha
		}
	}

	return result, nil
}

func writeCachedConfig(yamlPath, metadataPath string, data []byte, env *Config, sha string) error {
	if err := utils.AtomicWrite(yamlPath, data, 0600); err != nil {
		return fmt.Errorf("write config cache: %w", err)
	}
	metadata := configMetadata{
		SourceURL: env.ConfigRepoURL,
		Branch:    env.ConfigRepoBranch,
		CommitSha: sha,
		FetchedAt: time.Now().UTC(),
	}
	cachedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal config metadata: %w", err)
	}
	if err := utils.AtomicWrite(metadataPath, cachedMetadata, 0600); err != nil {
		return fmt.Errorf("write config metadata: %w", err)
	}
	return nil
}
