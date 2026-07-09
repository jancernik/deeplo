// Package daemon implements deeplo's long-running service process.
// It owns startup and shutdown, coordinates configuration reloads, and keeps
// deployment intake, reconciliation, HTTP, and admin-socket services running.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jancernik/deeplo/internal/api"
	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/build"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/engine"
	"github.com/jancernik/deeplo/internal/logger"
	"github.com/jancernik/deeplo/internal/mirror"
	"github.com/jancernik/deeplo/internal/planner"
	"github.com/jancernik/deeplo/internal/poller"
	"github.com/jancernik/deeplo/internal/providers"
	"github.com/jancernik/deeplo/internal/repowatcher"
	"github.com/jancernik/deeplo/internal/runlog"
	"github.com/jancernik/deeplo/internal/runner"
	"github.com/jancernik/deeplo/internal/ssh"
	"github.com/jancernik/deeplo/internal/state"
	"github.com/jancernik/deeplo/internal/utils"
)

type App struct {
	env           *bootstrap.Config
	logger        *slog.Logger
	config        atomic.Pointer[config.Config]
	store         *state.FileStore
	runner        *runner.Runner
	watcher       *managedWatcher
	reloader      *configReloader
	adminServer   *api.Server
	logServer     *http.Server
	deployHandler func(context.Context, planner.RepoEvent)
	reconcileLoop *reconcileLoop
	intakeCancel  context.CancelFunc
	opsCancel     context.CancelFunc
	lockFile      *os.File
}

func New() *App {
	return &App{}
}

func (app *App) Run(ctx context.Context) error {
	startedAt := time.Now()

	app.env = bootstrap.LoadEnv()
	app.logger = logger.New(app.env.LogLevel, app.env.LogFormat, app.env.LogColor)

	app.logger.Info("deeplo daemon starting",
		"version", build.Version,
		"config_source", app.env.Source,
	)

	if errs := app.env.Validate(); len(errs) > 0 {
		for _, err := range errs {
			app.logger.Error("bootstrap config error", "env_var", err.Field, "reason", err.Message)
		}
		return fmt.Errorf("invalid bootstrap config")
	}

	if err := os.MkdirAll(app.env.DataPath, 0750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	lockFile, err := acquireSingleInstanceLock(app.env.DataPath)
	if err != nil {
		return err
	}
	app.lockFile = lockFile

	configResult, err := bootstrap.LoadConfig(ctx, app.env, app.logger.With("component", "config_loader"))
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if configResult.FromCache {
		app.logger.Warn("running from last-known-good cached config",
			"config_source", app.env.Source, "repo", app.env.ConfigRepoURL)
	}
	if configResult.CommitSha != "" && !configResult.FromCache {
		app.logger.Info("config loaded from git",
			"repo", app.env.ConfigRepoURL, "branch", app.env.ConfigRepoBranch, "sha", utils.ShortSha(configResult.CommitSha))
	}

	warnIncompleteConfig(app.logger, configResult.Config)

	app.config.Store(configResult.Config)

	app.store = state.NewFileStore(app.env.DataPath)
	if err := app.store.Init(); err != nil {
		return fmt.Errorf("init store: %w", err)
	}
	if stale, err := app.store.MarkStaleRunningDeploymentsFailed(); err != nil {
		app.logger.Warn("could not mark stale deployments as failed", "err", err)
	} else if stale > 0 {
		app.logger.Warn("marked stale running deployments as failed", "count", stale)
	}

	reposDir := filepath.Join(app.env.DataPath, "repos")
	if err := os.MkdirAll(reposDir, 0755); err != nil {
		return fmt.Errorf("create repos dir: %w", err)
	}

	logsDir := filepath.Join(app.env.DataPath, "runs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return fmt.Errorf("create runs dir: %w", err)
	}

	app.runner = runner.New(runner.Config{
		MaxWorkers:         app.env.MaxWorkers,
		MaxHostConcurrency: app.env.MaxHostConcurrency,
	}, app.logger)
	app.runner.Start()

	go engine.DrainResults(app.runner.Results())

	dialer := ssh.NewDialer()
	sshEnv := mirror.SshEnv(app.env.SSHPrivateKeyFile, app.env.SSHKnownHosts, app.env.SSHHostKeyPolicy)

	reporter, err := providers.BuildReporter(app.env, app.logger)
	if err != nil {
		return fmt.Errorf("build reporter: %w", err)
	}
	providers.WarnAmbiguousReporting(app.env, configResult.Config, app.logger)

	getConfig := func() *config.Config { return app.config.Load() }

	getMirrorHead := func(repoURL, branch string) (string, bool) {
		return resolveMirrorHead(repoURL, branch, app.env.DataPath, app.env.Source, sshEnv, app.logger)
	}

	findMirror := func(repoURL string) engine.MirrorDiffer {
		return resolveMirror(repoURL, app.env.DataPath, app.env.Source, sshEnv, app.logger)
	}

	// Reloads the deploy config when an event targets the config repo.
	//  Returns an error when the config can't be loaded, used to defer the deploy.
	configRepoFullName := ""
	if app.env.Source == bootstrap.SourceGit {
		configRepoFullName = engine.RepoFullName(app.env.ConfigRepoURL)
	}
	reloadConfigRepo := func(ctx context.Context, repoName string) error {
		if isConfigRepo(repoName, configRepoFullName, getConfig().Repos) {
			return app.reloader.Reload(ctx)
		}
		return nil
	}

	// opsCtx scopes all deploy and teardown work
	// intakeCtx scopes the reconcile loop worker and the local config file watcher
	opsCtx, opsCancel := context.WithCancel(ctx)
	intakeCtx, intakeCancel := context.WithCancel(ctx)
	app.opsCancel = opsCancel
	app.intakeCancel = intakeCancel

	// Performs the teardown/deploy work for a single config change.
	// Runs under opsCtx so in-flight work finishes during the shutdown grace period.
	reconcileOnce := func(_ context.Context, oldConfig, newConfig *config.Config) {
		engine.ReconcileRemovals(opsCtx, oldConfig, newConfig, app.env, app.store, dialer, app.runner, getConfig, app.logger)
		engine.ReconcileAdditions(opsCtx, oldConfig, newConfig, app.store, getMirrorHead, app.deployHandler, app.logger)
		engine.ReconcileProjectChanges(opsCtx, oldConfig, newConfig, app.store, app.deployHandler, app.logger)
	}

	onConfigReload := func(_, newConfig *config.Config) {
		app.watcher.restart(opsCtx, buildWatcher(newConfig, getConfig, app.env, app.reloader, app.deployHandler, reloadConfigRepo, app.store, sshEnv, app.logger))
		app.logger.Info("watcher restarted with reloaded config")
		app.reconcileLoop.Trigger()
	}

	app.reloader = newConfigReloader(app.env, &app.config, onConfigReload, app.logger)

	app.deployHandler = engine.MakePushHandler(getConfig, app.env, app.runner, dialer, app.store, reporter, sshEnv, app.logger)

	previousConfig, err := bootstrap.LoadAppliedConfig(app.env.DataPath)
	if err != nil {
		app.logger.Warn("could not load previous applied config, treating as first start", "err", err)
		previousConfig = nil
	}
	app.reconcileLoop = newReconcileLoop(
		getConfig,
		previousConfig,
		reconcileOnce,
		func(applied *config.Config) {
			if saveErr := bootstrap.SaveAppliedConfig(app.env.DataPath, applied); saveErr != nil {
				app.logger.Warn("could not save applied config", "err", saveErr)
			}
		},
		app.logger,
	)
	app.reconcileLoop.Start(intakeCtx)
	app.reconcileLoop.Trigger()

	engine.ResumeIncompleteDeploys(opsCtx, configResult.Config, app.store, getMirrorHead, findMirror, app.deployHandler, app.logger)

	app.watcher = &managedWatcher{}
	app.watcher.start(buildWatcher(configResult.Config, getConfig, app.env, app.reloader, app.deployHandler, reloadConfigRepo, app.store, sshEnv, app.logger), opsCtx)

	switch {
	case app.env.Source == bootstrap.SourceGit:
		interval := repowatcher.DefaultPollInterval
		if app.env.ConfigRepoInterval > 0 {
			interval = app.env.ConfigRepoInterval
		}
		switch app.env.ConfigRepoMode {
		case "webhook":
			app.logger.Info("config reloader started", "mode", "webhook",
				"repo", app.env.ConfigRepoURL, "branch", app.env.ConfigRepoBranch)
		case "hybrid":
			app.logger.Info("config reloader started", "mode", "hybrid",
				"repo", app.env.ConfigRepoURL, "branch", app.env.ConfigRepoBranch, "interval", interval)
		default:
			app.logger.Info("config reloader started", "mode", "poll",
				"repo", app.env.ConfigRepoURL, "branch", app.env.ConfigRepoBranch, "interval", interval)
		}
	case app.env.ConfigWatch:
		app.reloader.StartLocal(intakeCtx)
	}

	app.adminServer = api.New(api.Config{
		SocketPath: app.env.UnixSocket,
		StartedAt:  startedAt,
		Version:    build.Version,
		GetConfig:  getConfig,
		Bootstrap:  app.env,
		Store:      app.store,
		RunsDir:    logsDir,
		OnReload:   app.reloader.Reload,
		OnRefresh:  buildRefreshFunc(app.env, getConfig, app.logger),
		OnProbe:    buildProbeFunc(app.env, getConfig),
		OnDeploy:   buildDeployFunc(opsCtx, getConfig, app.store, getMirrorHead, app.deployHandler, app.logger),
		Logger:     app.logger,
	})
	if err := app.adminServer.Start(); err != nil {
		return fmt.Errorf("admin server: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok") //nolint:errcheck
	})

	onPush := engine.MakeWebhookPushHandler(getConfig, app.store, app.deployHandler, reloadConfigRepo, app.logger)
	if err := providers.RegisterWebhooks(mux, app.env, getConfig(), opsCtx, onPush, app.logger); err != nil {
		return fmt.Errorf("register webhooks: %w", err)
	}

	app.logServer = newRunLogServer(app.env, logsDir, mux)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", app.env.HTTPPort),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	if app.env.LogRetentionDays > 0 {
		historyDir := filepath.Join(app.env.DataPath, "history", "runs")
		runlog.StartCleanup(ctx, app.env.LogRetentionDays, app.logger.With("component", "cleanup"), logsDir, historyDir)
	}

	// Signal Handling & Shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		app.logger.Info("listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- fmt.Errorf("http server: %w", err)
		}
	}()

	if app.logServer != nil {
		go func() {
			app.logger.Info("serving run logs", "addr", app.logServer.Addr)
			if err := app.logServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				serverErr <- fmt.Errorf("log server: %w", err)
			}
		}()
	}

	select {
	case err := <-serverErr:
		app.shutdown(httpServer)
		return err
	case sig := <-quit:
		app.logger.Info("received shutdown signal", "signal", sig)
		app.shutdown(httpServer)
	}

	return nil
}

func newRunLogServer(env *bootstrap.Config, logsDir string, mux *http.ServeMux) *http.Server {
	if !env.LogServerEnabled() {
		return nil
	}
	if env.LogServerListenPort() == env.HTTPPort {
		mux.Handle("/runs/", runlog.Handler(logsDir))
		return nil
	}
	logMux := http.NewServeMux()
	logMux.Handle("/runs/", runlog.Handler(logsDir))
	return &http.Server{
		Addr:         fmt.Sprintf(":%d", env.LogServerListenPort()),
		Handler:      logMux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

func warnIncompleteConfig(logger *slog.Logger, deployConfig *config.Config) {
	for _, issue := range deployConfig.BlockingIssues() {
		logger.Warn("config incomplete, daemon running but cannot deploy until fixed",
			"field", issue.Field, "reason", issue.Message)
	}
}

func (app *App) shutdown(httpServer *http.Server) {
	// Stop intake.
	app.watcher.stop()
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if app.adminServer != nil {
		if err := app.adminServer.Shutdown(shutCtx); err != nil {
			app.logger.Warn("admin server shutdown error", "err", err)
		}
	}
	if err := httpServer.Shutdown(shutCtx); err != nil {
		app.logger.Warn("http server shutdown error", "err", err)
	}
	if app.logServer != nil {
		if err := app.logServer.Shutdown(shutCtx); err != nil {
			app.logger.Warn("log server shutdown error", "err", err)
		}
	}
	if app.intakeCancel != nil {
		app.intakeCancel()
	}

	// Arm the grace deadline. When it fires, opsCtx is cancelled
	var graceTimer *time.Timer
	if app.env.ShutdownGrace > 0 {
		app.logger.Info("draining in-flight deploys", "grace", app.env.ShutdownGrace)
		graceTimer = time.AfterFunc(app.env.ShutdownGrace, func() {
			app.logger.Warn("shutdown grace elapsed, cancelling in-flight deploys")
			if app.opsCancel != nil {
				app.opsCancel()
			}
		})
	} else if app.opsCancel != nil {
		app.opsCancel()
	}

	// Wait for the reconcile worker to finish its current pass and exit,
	if app.reconcileLoop != nil {
		app.reconcileLoop.Wait()
	}
	app.runner.Stop()

	if graceTimer != nil {
		graceTimer.Stop()
	}
	if app.opsCancel != nil {
		app.opsCancel()
	}

	if app.lockFile != nil {
		_ = app.lockFile.Close()
	}
	app.logger.Info("shutdown complete")
}

func buildWatcher(
	deployConfig *config.Config,
	getConfig func() *config.Config,
	env *bootstrap.Config,
	reloader *configReloader,
	deployHandler func(context.Context, planner.RepoEvent),
	reloadConfigRepo func(ctx context.Context, repoName string) error,
	store *state.FileStore,
	sshEnv []string,
	logger *slog.Logger,
) *repowatcher.Watcher {
	configMirrorPath := ""
	if env.Source == bootstrap.SourceGit {
		configMirrorPath = filepath.Join(env.DataPath, "config")
	}
	deployPoller := poller.New(deployConfig, getConfig, env.DataPath, configMirrorPath, store, deployHandler, reloadConfigRepo, logger, sshEnv)
	subs := deployPoller.Subscriptions()

	if env.Source == bootstrap.SourceGit && env.ConfigRepoMode != "webhook" {
		interval := env.ConfigRepoInterval
		if interval <= 0 {
			interval = repowatcher.DefaultPollInterval
		}
		configSub := repowatcher.Subscription{
			URL:      env.ConfigRepoURL,
			Branch:   env.ConfigRepoBranch,
			Interval: interval,
			Handler: func(ctx context.Context, _ string) {
				reloader.fetchAndApply(ctx)
			},
		}
		subs = append([]repowatcher.Subscription{configSub}, subs...)
	}

	return repowatcher.New(subs, mirror.RemoteSha, sshEnv, logger)
}

type managedWatcher struct {
	mutex    sync.Mutex
	instance *repowatcher.Watcher
}

func (managed *managedWatcher) start(instance *repowatcher.Watcher, appCtx context.Context) {
	if instance == nil {
		return
	}
	instance.Start(appCtx)
	managed.mutex.Lock()
	managed.instance = instance
	managed.mutex.Unlock()
}

func (managed *managedWatcher) stop() {
	managed.mutex.Lock()
	instance := managed.instance
	managed.mutex.Unlock()
	if instance != nil {
		instance.Stop()
	}
}

func (managed *managedWatcher) restart(appCtx context.Context, instance *repowatcher.Watcher) {
	managed.mutex.Lock()
	old := managed.instance
	managed.instance = instance
	managed.mutex.Unlock()
	// Start the new watcher before stopping the old one. Stopping must be
	// asynchronous because restart may be called from inside a watcher goroutine
	if instance != nil {
		instance.Start(appCtx)
	}
	if old != nil {
		go old.Stop()
	}
}

// Returns the branch tip SHA from the best available local mirror.
func resolveMirrorHead(repoURL, branch, dataPath string, source bootstrap.Source, sshEnv []string, logger *slog.Logger) (string, bool) {
	if source == bootstrap.SourceGit {
		configRepo, _ := mirror.Find(repoURL, filepath.Join(dataPath, "config"), sshEnv, logger)
		if configRepo != nil {
			if head, err := configRepo.LocalHead(branch); err == nil {
				return head, true
			}
		}
	}
	deployRepo, _ := mirror.Find(repoURL, dataPath, sshEnv, logger)
	if deployRepo != nil {
		if head, err := deployRepo.LocalHead(branch); err == nil {
			return head, true
		}
	}
	return "", false
}

// Returns the best available local mirror for repoURL, following the same lookup
// order as resolveMirrorHead.
func resolveMirror(repoURL, dataPath string, source bootstrap.Source, sshEnv []string, logger *slog.Logger) engine.MirrorDiffer {
	if source == bootstrap.SourceGit {
		if configRepo, _ := mirror.Find(repoURL, filepath.Join(dataPath, "config"), sshEnv, logger); configRepo != nil {
			return configRepo
		}
	}
	if deployRepo, _ := mirror.Find(repoURL, dataPath, sshEnv, logger); deployRepo != nil {
		return deployRepo
	}
	return nil
}

func isConfigRepo(repoName, configRepoFullName string, repos []config.RepoConfig) bool {
	if configRepoFullName == "" {
		return false
	}
	for _, repo := range repos {
		if repo.Name == repoName && engine.RepoFullName(repo.URL) == configRepoFullName {
			return true
		}
	}
	return false
}
