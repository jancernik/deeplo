package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
)

func TestConfigWatcher_StartLocal_StopsOnCancel(t *testing.T) {
	w := newConfigReloader(&bootstrap.Config{Source: bootstrap.SourceLocal, ConfigFile: "/nonexistent/config.yml"}, nil, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	w.StartLocal(ctx) // goroutine starts but won't fire for 10 min

	// Cancelling ctx should stop the goroutine without hanging.
	cancel()
}

func TestConfigWatcher_StartLocal_ReloadsOnFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")

	initial := []byte(`
version: 1
hosts:
  - name: h1
    address: 10.0.0.1
    deploy_dir: /srv/apps
repos:
  - name: r1
    url: git@github.com:org/repo.git
    trigger_mode: webhook
projects:
  - name: p1
    repo: r1
    repo_subdir: apps/p1
    targets: [h1]
`)
	updated := []byte(`
version: 1
hosts:
  - name: h1
    address: 10.0.0.1
    deploy_dir: /srv/apps
repos:
  - name: r1
    url: git@github.com:org/repo.git
    trigger_mode: poll
projects:
  - name: p1
    repo: r1
    repo_subdir: apps/p1
    targets: [h1]
`)

	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	initialConfig, err := config.Load(path)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}

	var configPtr atomic.Pointer[config.Config]
	configPtr.Store(initialConfig)
	reloaded := make(chan *config.Config, 1)
	w := newConfigReloader(
		&bootstrap.Config{Source: bootstrap.SourceLocal, ConfigFile: path},
		&configPtr,
		func(_ *config.Config, newConfig *config.Config) {
			reloaded <- newConfig
		},
		slog.Default(),
	)

	w.StartLocal(t.Context())

	tmp := filepath.Join(dir, "config.yml.tmp")
	if err := os.WriteFile(tmp, updated, 0600); err != nil {
		t.Fatalf("write updated config: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename updated config: %v", err)
	}

	select {
	case newConfig := <-reloaded:
		if newConfig.Repos[0].TriggerMode != config.TriggerModePoll {
			t.Fatalf("reloaded trigger_mode = %q, want poll", newConfig.Repos[0].TriggerMode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for local config watcher reload")
	}
}

// The apply mutex prevents reloads from interleaving: onReload never runs
// concurrently and each invocation sees the previous reload's config as baseline.
func TestApplyConfigChange_SerializesConcurrentReloads(t *testing.T) {
	// validConfig builds a config that passes validation, varying only the
	// project name so distinct markers are not DeepEqual.
	validConfig := func(marker string) *config.Config {
		return &config.Config{
			Hosts: []config.Host{{Name: "h1", Address: "1.2.3.4", DeployDir: "/srv"}},
			Repos: []config.RepoConfig{{Name: "r1", URL: "git@github.com:o/r.git", Branch: "main", TriggerMode: config.TriggerModeWebhook}},
			Projects: []config.Project{{
				Name: marker, Repo: "r1", RepoSubdir: "sub",
				ComposeFiles: []string{"docker-compose.yml"}, Targets: []string{"h1"},
			}},
		}
	}

	var current atomic.Pointer[config.Config]
	current.Store(validConfig("v0"))

	var inReload atomic.Int32
	var maxConcurrent atomic.Int32
	var calls atomic.Int32

	reloader := newConfigReloader(
		&bootstrap.Config{Source: bootstrap.SourceLocal},
		&current,
		func(oldConfig, newConfig *config.Config) {
			n := inReload.Add(1)
			for {
				max := maxConcurrent.Load()
				if n <= max || maxConcurrent.CompareAndSwap(max, n) {
					break
				}
			}
			// Hold the section briefly to widen any interleaving window.
			time.Sleep(time.Millisecond)
			calls.Add(1)
			inReload.Add(-1)
		},
		slog.Default(),
	)

	const goroutines = 20
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			result := &bootstrap.ConfigResult{Config: validConfig("v" + strconv.Itoa(n+1))}
			if err := reloader.applyConfigChange(result); err != nil {
				t.Errorf("applyConfigChange: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if max := maxConcurrent.Load(); max != 1 {
		t.Errorf("onReload ran concurrently (max %d); reloads are not serialized", max)
	}
	// Every distinct config differs from the prior, so all 20 should apply.
	if got := calls.Load(); got != goroutines {
		t.Errorf("onReload called %d times, want %d", got, goroutines)
	}
}

// A live reload to an invalid config is rejected: the running config is kept and
// onReload is not called, so a bad live edit can never replace a working config.
func TestApplyConfigChange_InvalidConfigKeepsCurrent(t *testing.T) {
	valid := &config.Config{
		Hosts: []config.Host{{Name: "h1", Address: "1.2.3.4", DeployDir: "/srv"}},
		Repos: []config.RepoConfig{{Name: "r1", URL: "git@github.com:o/r.git", Branch: "main", TriggerMode: config.TriggerModeWebhook}},
		Projects: []config.Project{{
			Name: "p1", Repo: "r1", RepoSubdir: "sub",
			ComposeFiles: []string{"docker-compose.yml"}, Targets: []string{"h1"},
		}},
	}

	var current atomic.Pointer[config.Config]
	current.Store(valid)

	var reloadCalled atomic.Bool
	reloader := newConfigReloader(
		&bootstrap.Config{Source: bootstrap.SourceLocal},
		&current,
		func(_, _ *config.Config) { reloadCalled.Store(true) },
		slog.Default(),
	)

	// An empty config is invalid (no hosts/repos/projects).
	err := reloader.applyConfigChange(&bootstrap.ConfigResult{Config: &config.Config{}})
	if err == nil {
		t.Fatal("expected applyConfigChange to reject an invalid config")
	}
	if current.Load() != valid {
		t.Error("running config was replaced by an invalid reload")
	}
	if reloadCalled.Load() {
		t.Error("onReload should not run for a rejected config")
	}
}

// A reload keeping valid hosts and repos but removing all projects is applied
// (not rejected), so the following reconcile tears the deployments down.
func TestApplyConfigChange_NoProjectsAppliesAndTearsDown(t *testing.T) {
	valid := &config.Config{
		Hosts: []config.Host{{Name: "h1", Address: "1.2.3.4", DeployDir: "/srv"}},
		Repos: []config.RepoConfig{{Name: "r1", URL: "git@github.com:o/r.git", Branch: "main", TriggerMode: config.TriggerModeWebhook}},
		Projects: []config.Project{{
			Name: "p1", Repo: "r1", RepoSubdir: "sub",
			ComposeFiles: []string{"docker-compose.yml"}, Targets: []string{"h1"},
		}},
	}
	noProjects := &config.Config{
		Hosts: valid.Hosts,
		Repos: valid.Repos,
	}

	var current atomic.Pointer[config.Config]
	current.Store(valid)

	var reloadCalled atomic.Bool
	reloader := newConfigReloader(
		&bootstrap.Config{Source: bootstrap.SourceLocal},
		&current,
		func(_, _ *config.Config) { reloadCalled.Store(true) },
		slog.Default(),
	)

	if err := reloader.applyConfigChange(&bootstrap.ConfigResult{Config: noProjects}); err != nil {
		t.Fatalf("applyConfigChange should accept a hosts+repos no-projects config: %v", err)
	}
	if current.Load() != noProjects {
		t.Error("running config should advance to the no-projects config")
	}
	if !reloadCalled.Load() {
		t.Error("onReload should run so the reconcile tears the removed projects down")
	}
}
