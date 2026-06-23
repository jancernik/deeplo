package daemon

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/engine"
	"github.com/jancernik/deeplo/internal/runner"
)

// newShutdownTestApp builds a minimal App wired for exercising shutdown: a real
// runner and reconcile loop, the two shutdown contexts, and a no-op watcher. It
// returns the opsCtx that deploy jobs must be submitted under so they observe
// the shutdown grace/cancellation.
func newShutdownTestApp(t *testing.T, grace time.Duration) (*App, context.Context) {
	t.Helper()

	app := &App{
		logger:  slog.Default(),
		env:     &bootstrap.Config{ShutdownGrace: grace},
		watcher: &managedWatcher{},
	}
	app.runner = runner.New(runner.Config{MaxWorkers: 4, MaxHostConcurrency: 4}, slog.Default())
	app.runner.Start()
	go engine.DrainResults(app.runner.Results())

	opsCtx, opsCancel := context.WithCancel(context.Background())
	intakeCtx, intakeCancel := context.WithCancel(context.Background())
	app.opsCancel = opsCancel
	app.intakeCancel = intakeCancel

	deployConfig := &config.Config{}
	app.reconcileLoop = newReconcileLoop(
		func() *config.Config { return deployConfig },
		deployConfig, // applied == current → the loop never reconciles in these tests
		func(context.Context, *config.Config, *config.Config) {},
		nil,
		slog.Default(),
	)
	app.reconcileLoop.Start(intakeCtx)

	return app, opsCtx
}

// An in-flight deploy that finishes on its own must be allowed to complete, and
// shutdown must return as soon as it does - not wait out the whole grace.
func TestShutdown_WaitsForInFlightThenReturns(t *testing.T) {
	app, opsCtx := newShutdownTestApp(t, 5*time.Second)

	started := make(chan struct{})
	finished := make(chan struct{})
	if err := app.runner.Submit(opsCtx, runner.Job{
		ID: "deploy", Project: "p", Host: "h",
		DeployFunc: func(context.Context) error {
			close(started)
			time.Sleep(50 * time.Millisecond)
			close(finished)
			return nil
		},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-started

	begin := time.Now()
	app.shutdown(&http.Server{})
	elapsed := time.Since(begin)

	select {
	case <-finished:
	default:
		t.Error("in-flight deploy was not allowed to finish before shutdown returned")
	}
	if elapsed >= 5*time.Second {
		t.Errorf("shutdown waited the full grace (%s); it should return once the deploy finished", elapsed)
	}
}

// A deploy that hangs past the grace must be cancelled (via opsCtx), and total
// shutdown time must stay bounded by the grace rather than blocking forever.
func TestShutdown_AbortsHungDeployAfterGrace(t *testing.T) {
	const grace = 150 * time.Millisecond
	app, opsCtx := newShutdownTestApp(t, grace)

	started := make(chan struct{})
	cancelled := make(chan struct{})
	if err := app.runner.Submit(opsCtx, runner.Job{
		ID: "hung", Project: "p", Host: "h",
		DeployFunc: func(ctx context.Context) error {
			close(started)
			<-ctx.Done() // only unblocks when the grace deadline cancels opsCtx
			close(cancelled)
			return ctx.Err()
		},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-started

	begin := time.Now()
	app.shutdown(&http.Server{})
	elapsed := time.Since(begin)

	select {
	case <-cancelled:
	default:
		t.Error("hung deploy was not cancelled on shutdown")
	}
	if elapsed < grace {
		t.Errorf("shutdown returned before the grace elapsed (%s < %s)", elapsed, grace)
	}
	if elapsed > 2*time.Second {
		t.Errorf("shutdown was not bounded: took %s", elapsed)
	}
}

// A zero grace aborts in-flight work immediately instead of waiting.
func TestShutdown_ZeroGraceAbortsImmediately(t *testing.T) {
	app, opsCtx := newShutdownTestApp(t, 0)

	started := make(chan struct{})
	cancelled := make(chan struct{})
	if err := app.runner.Submit(opsCtx, runner.Job{
		ID: "hung", Project: "p", Host: "h",
		DeployFunc: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return ctx.Err()
		},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-started

	begin := time.Now()
	app.shutdown(&http.Server{})
	elapsed := time.Since(begin)

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Error("zero-grace shutdown did not cancel in-flight deploy")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("zero-grace shutdown should abort promptly, took %s", elapsed)
	}
}
