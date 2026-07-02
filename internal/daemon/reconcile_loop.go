package daemon

import (
	"context"
	"log/slog"
	"reflect"
	"sync"

	"github.com/jancernik/deeplo/internal/config"
)

// Serializes config reconciliations behind a single worker goroutine.
type reconcileLoop struct {
	getConfig func() *config.Config
	reconcile func(ctx context.Context, oldConfig, newConfig *config.Config)
	persist   func(newConfig *config.Config)
	logger    *slog.Logger
	triggers  chan struct{}
	applied   *config.Config
	waitGroup sync.WaitGroup
}

func newReconcileLoop(
	getConfig func() *config.Config,
	initialApplied *config.Config,
	reconcile func(ctx context.Context, oldConfig, newConfig *config.Config),
	persist func(newConfig *config.Config),
	logger *slog.Logger,
) *reconcileLoop {
	return &reconcileLoop{
		getConfig: getConfig,
		reconcile: reconcile,
		persist:   persist,
		logger:    logger.With("component", "reconcile_loop"),
		triggers:  make(chan struct{}, 1),
		applied:   initialApplied,
	}
}

func (loop *reconcileLoop) Start(ctx context.Context) {
	loop.waitGroup.Add(1)
	go loop.run(ctx)
}

func (loop *reconcileLoop) Trigger() {
	select {
	case loop.triggers <- struct{}{}:
	default:
	}
}

func (loop *reconcileLoop) Wait() {
	loop.waitGroup.Wait()
}

func (loop *reconcileLoop) run(ctx context.Context) {
	defer loop.waitGroup.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-loop.triggers:
			loop.pass(ctx)
		}
	}
}

func (loop *reconcileLoop) pass(ctx context.Context) {
	current := loop.getConfig()
	if current == nil {
		return
	}

	// Never reconcile against a broken/unconfigured config.
	if issues := current.BlockingIssues(); len(issues) > 0 {
		loop.logger.Warn("config incomplete, skipping reconcile and keeping existing deployments",
			"issues", len(issues))
		return
	}

	switch {
	case loop.applied == nil:
		loop.logger.Debug("adopting initial config baseline without reconcile")
	case reflect.DeepEqual(loop.applied, current):
		return
	default:
		loop.reconcile(ctx, loop.applied, current)
	}

	loop.applied = current
	if loop.persist != nil {
		loop.persist(current)
	}
}
