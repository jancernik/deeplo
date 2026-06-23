package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/utils"
)

type configReloader struct {
	env        *bootstrap.Config
	config     *atomic.Pointer[config.Config]
	onReload   func(oldConfig, newConfig *config.Config)
	logger     *slog.Logger
	applyMutex sync.Mutex
}

func newConfigReloader(env *bootstrap.Config, config *atomic.Pointer[config.Config], onReload func(oldConfig, newConfig *config.Config), logger *slog.Logger) *configReloader {
	return &configReloader{
		env:      env,
		config:   config,
		onReload: onReload,
		logger:   logger.With("component", "config_reloader"),
	}
}

func (reloader *configReloader) StartLocal(ctx context.Context) {
	reloader.logger.Info("started", "mode", "local_file_watch", "path", reloader.env.ConfigFile)

	dir := filepath.Dir(reloader.env.ConfigFile)
	file := filepath.Base(reloader.env.ConfigFile)
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		reloader.logger.Warn("failed to start local file watcher", "path", reloader.env.ConfigFile, "err", err)
		return
	}
	if err := fsWatcher.Add(dir); err != nil {
		_ = fsWatcher.Close()
		reloader.logger.Warn("failed to watch local config directory", "path", reloader.env.ConfigFile, "err", err)
		return
	}

	go func() {
		defer func() {
			if err := fsWatcher.Close(); err != nil {
				reloader.logger.Warn("close config watcher", "err", err)
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-fsWatcher.Events:
				if !ok {
					return
				}
				if filepath.Base(evt.Name) != file {
					continue
				}
				if evt.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
					continue
				}
				reloader.fetchAndApply(ctx)
			case err, ok := <-fsWatcher.Errors:
				if !ok {
					return
				}
				reloader.logger.Warn("local file watcher error", "path", reloader.env.ConfigFile, "err", err)
			}
		}
	}()
}

// Validates newConfig and, if valid and different from the current config, swaps it in and calls the onReload callback.
func (reloader *configReloader) applyConfigChange(result *bootstrap.ConfigResult) error {
	reloader.applyMutex.Lock()
	defer reloader.applyMutex.Unlock()

	oldConfig := reloader.config.Load()
	newConfig := result.Config
	if reflect.DeepEqual(oldConfig, newConfig) {
		reloader.logger.Debug("no change")
		return nil
	}
	errs := newConfig.BlockingIssues()
	for _, issue := range errs {
		reloader.logger.Error("reloaded config is invalid, keeping current config",
			"field", issue.Field, "reason", issue.Message)
	}
	if len(errs) > 0 {
		return fmt.Errorf("config validation failed")
	}
	if result.CommitSha != "" {
		reloader.logger.Info("config changed, reloading",
			"repo", reloader.env.ConfigRepoURL, "branch", reloader.env.ConfigRepoBranch, "sha", utils.ShortSha(result.CommitSha))
	} else {
		reloader.logger.Info("config changed, reloading", "path", reloader.env.ConfigFile)
	}
	reloader.config.Store(newConfig)
	if reloader.onReload != nil {
		reloader.onReload(oldConfig, newConfig)
	}
	return nil
}

func (reloader *configReloader) fetchAndApply(ctx context.Context) {
	result, err := bootstrap.LoadConfig(ctx, reloader.env, reloader.logger)
	if err != nil {
		reloader.logger.Warn("re-fetch failed", "err", err)
		return
	}
	if result.FromCache {
		return
	}
	if err := reloader.applyConfigChange(result); err != nil {
		reloader.logger.Warn("reload failed", "err", err)
	}
}

// Triggers an immediate config check and applies any changes.
func (reloader *configReloader) Reload(ctx context.Context) error {
	result, err := bootstrap.LoadConfig(ctx, reloader.env, reloader.logger)
	if err != nil {
		return fmt.Errorf("config fetch failed: %w", err)
	}
	if result.FromCache {
		return fmt.Errorf("config unavailable: running from last-known-good cache")
	}
	return reloader.applyConfigChange(result)
}
