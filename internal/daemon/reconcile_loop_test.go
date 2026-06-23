package daemon

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/config"
)

// configWith returns a valid config with a single project named after marker,
// so distinct markers produce non-equal configs that still pass Validate.
func configWith(marker string) *config.Config {
	return &config.Config{
		Hosts: []config.Host{{Name: "h1", Address: "1.2.3.4", DeployDir: "/srv"}},
		Repos: []config.RepoConfig{{Name: "r1", URL: "git@github.com:o/r.git", Branch: "main", TriggerMode: config.TriggerModeWebhook}},
		Projects: []config.Project{{
			Name: marker, Repo: "r1", RepoSubdir: "sub",
			ComposeFiles: []string{"docker-compose.yml"}, Targets: []string{"h1"},
		}},
	}
}

type reconcileCall struct{ oldMarker, newMarker string }

func markerOf(deployConfig *config.Config) string {
	if deployConfig == nil || len(deployConfig.Projects) == 0 {
		return ""
	}
	return deployConfig.Projects[0].Name
}

// recordingReconciler records each reconcile call and gates completion so a
// test can observe in-flight passes.
type recordingReconciler struct {
	mutex       sync.Mutex
	calls       []reconcileCall
	persisted   []string
	started     chan struct{} // one signal per reconcile entry
	gate        chan struct{} // each reconcile waits for one token before returning
	concurrent  atomic.Int32
	maxParallel atomic.Int32
}

func newRecordingReconciler() *recordingReconciler {
	return &recordingReconciler{
		started: make(chan struct{}, 16),
		gate:    make(chan struct{}),
	}
}

func (recorder *recordingReconciler) reconcile(_ context.Context, oldConfig, newConfig *config.Config) {
	now := recorder.concurrent.Add(1)
	for {
		max := recorder.maxParallel.Load()
		if now <= max || recorder.maxParallel.CompareAndSwap(max, now) {
			break
		}
	}
	recorder.started <- struct{}{}
	<-recorder.gate
	recorder.mutex.Lock()
	recorder.calls = append(recorder.calls, reconcileCall{markerOf(oldConfig), markerOf(newConfig)})
	recorder.mutex.Unlock()
	recorder.concurrent.Add(-1)
}

func (recorder *recordingReconciler) persistFn(newConfig *config.Config) {
	recorder.mutex.Lock()
	recorder.persisted = append(recorder.persisted, markerOf(newConfig))
	recorder.mutex.Unlock()
}

func (recorder *recordingReconciler) release() { recorder.gate <- struct{}{} }

func (recorder *recordingReconciler) recordedCalls() []reconcileCall {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return append([]reconcileCall(nil), recorder.calls...)
}

func (recorder *recordingReconciler) recordedPersists() []string {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return append([]string(nil), recorder.persisted...)
}

func newConfigHolder(initial *config.Config) (func() *config.Config, func(*config.Config)) {
	var pointer atomic.Pointer[config.Config]
	pointer.Store(initial)
	return pointer.Load, pointer.Store
}

// A burst of triggers during an in-flight pass coalesces into one follow-up
// against the latest config, and the baseline advances correctly.
func TestReconcileLoop_CoalescesAndTracksBaseline(t *testing.T) {
	getConfig, setConfig := newConfigHolder(configWith("V1"))
	recorder := newRecordingReconciler()
	loop := newReconcileLoop(getConfig, configWith("V0"), recorder.reconcile, recorder.persistFn, slog.Default())

	loop.Start(t.Context())

	loop.Trigger()
	<-recorder.started // pass is now in flight, blocked on the gate

	// Pushed while blocked, these coalesce into one pass against the latest (V3).
	setConfig(configWith("V2"))
	loop.Trigger()
	setConfig(configWith("V3"))
	loop.Trigger()

	recorder.release() // finish V0 -> V1
	<-recorder.started // coalesced pass V1 -> V3 now in flight
	recorder.release() // finish V1 -> V3

	time.Sleep(50 * time.Millisecond) // no third pass should run

	calls := recorder.recordedCalls()
	want := []reconcileCall{{"V0", "V1"}, {"V1", "V3"}}
	if len(calls) != len(want) {
		t.Fatalf("got %d reconcile calls %v, want %v", len(calls), calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %v, want %v", i, calls[i], want[i])
		}
	}

	if persists := recorder.recordedPersists(); len(persists) != 2 || persists[0] != "V1" || persists[1] != "V3" {
		t.Errorf("persisted = %v, want [V1 V3]", persists)
	}
}

// The worker serializes passes: a second never begins while the first is in flight.
func TestReconcileLoop_NeverRunsConcurrently(t *testing.T) {
	getConfig, setConfig := newConfigHolder(configWith("A"))
	recorder := newRecordingReconciler()
	loop := newReconcileLoop(getConfig, configWith("base"), recorder.reconcile, recorder.persistFn, slog.Default())

	loop.Start(t.Context())

	loop.Trigger()
	<-recorder.started

	setConfig(configWith("B"))
	loop.Trigger()

	select {
	case <-recorder.started:
		t.Fatal("a second reconcile started while the first was still in flight")
	case <-time.After(80 * time.Millisecond):
	}

	recorder.release() // finish first
	<-recorder.started // second starts only now
	recorder.release() // finish second

	time.Sleep(30 * time.Millisecond)
	if max := recorder.maxParallel.Load(); max != 1 {
		t.Errorf("max parallel reconciles = %d, want 1", max)
	}
}

// With no prior baseline, the current config is adopted and persisted without
// force-reconciling the whole world.
func TestReconcileLoop_NilBaselineAdoptsWithoutReconcile(t *testing.T) {
	getConfig, _ := newConfigHolder(configWith("current"))
	recorder := newRecordingReconciler()
	loop := newReconcileLoop(getConfig, nil, recorder.reconcile, recorder.persistFn, slog.Default())

	loop.Start(t.Context())

	loop.Trigger()

	select {
	case <-recorder.started:
		t.Fatal("reconcile ran on nil baseline; expected silent adoption")
	case <-time.After(80 * time.Millisecond):
	}

	if persists := recorder.recordedPersists(); len(persists) != 1 || persists[0] != "current" {
		t.Errorf("persisted = %v, want [current]", persists)
	}
}

// After adopting a nil baseline, the next changed config reconciles from it.
func TestReconcileLoop_AdoptThenReconcile(t *testing.T) {
	getConfig, setConfig := newConfigHolder(configWith("V0"))
	recorder := newRecordingReconciler()
	loop := newReconcileLoop(getConfig, nil, recorder.reconcile, recorder.persistFn, slog.Default())

	loop.Start(t.Context())

	loop.Trigger() // adopt V0, no reconcile
	time.Sleep(30 * time.Millisecond)

	setConfig(configWith("V1"))
	loop.Trigger()
	<-recorder.started
	recorder.release()
	time.Sleep(30 * time.Millisecond)

	calls := recorder.recordedCalls()
	if len(calls) != 1 || calls[0] != (reconcileCall{"V0", "V1"}) {
		t.Fatalf("got %v, want one call {V0 V1}", calls)
	}
}

// A trigger with no config change neither reconciles nor persists.
func TestReconcileLoop_UnchangedConfigSkips(t *testing.T) {
	getConfig, _ := newConfigHolder(configWith("same"))
	recorder := newRecordingReconciler()
	loop := newReconcileLoop(getConfig, configWith("same"), recorder.reconcile, recorder.persistFn, slog.Default())

	loop.Start(t.Context())

	loop.Trigger()
	select {
	case <-recorder.started:
		t.Fatal("reconcile ran for an unchanged config")
	case <-time.After(80 * time.Millisecond):
	}
	if persists := recorder.recordedPersists(); len(persists) != 0 {
		t.Errorf("persisted = %v, want none for unchanged config", persists)
	}
}

// Cancelling the context stops the worker and Wait returns.
func TestReconcileLoop_ShutdownStopsWorker(t *testing.T) {
	getConfig, _ := newConfigHolder(configWith("x"))
	recorder := newRecordingReconciler()
	loop := newReconcileLoop(getConfig, configWith("base"), recorder.reconcile, recorder.persistFn, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	loop.Start(ctx)
	cancel()

	done := make(chan struct{})
	go func() { loop.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not exit after context cancellation")
	}
}

// Trigger is non-blocking even with no worker draining the channel.
func TestReconcileLoop_TriggerNeverBlocks(t *testing.T) {
	getConfig, _ := newConfigHolder(configWith("x"))
	recorder := newRecordingReconciler()
	loop := newReconcileLoop(getConfig, configWith("base"), recorder.reconcile, recorder.persistFn, slog.Default())

	done := make(chan struct{})
	go func() {
		for range 1000 {
			loop.Trigger()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Trigger blocked")
	}
}

// Safety guard: an incomplete config must not reconcile (which would tear down
// every deployment), and the baseline is preserved for a later valid config.
func TestReconcileLoop_SkipsReconcileForInvalidConfig(t *testing.T) {
	applied := configWith("p1")                       // valid baseline with a deployed project
	getConfig, _ := newConfigHolder(&config.Config{}) // current config is empty == invalid

	var reconcileCalled atomic.Bool
	loop := newReconcileLoop(
		getConfig,
		applied,
		func(context.Context, *config.Config, *config.Config) { reconcileCalled.Store(true) },
		nil,
		slog.Default(),
	)

	loop.pass(context.Background())

	if reconcileCalled.Load() {
		t.Error("reconcile (teardown) must not run for an invalid/empty config")
	}
	if loop.applied != applied {
		t.Error("applied baseline must be preserved when the config is invalid")
	}
}

// A valid config with hosts and repos but zero projects reconciles (tearing
// down all deployments) rather than being skipped as incomplete.
func TestReconcileLoop_ReconcilesTeardownAllWhenNoProjects(t *testing.T) {
	applied := configWith("p1") // a project was deployed
	current := &config.Config{  // hosts+repos kept, all projects removed
		Hosts: []config.Host{{Name: "h1", Address: "1.2.3.4", DeployDir: "/srv"}},
		Repos: []config.RepoConfig{{Name: "r1", URL: "git@github.com:o/r.git", Branch: "main", TriggerMode: config.TriggerModeWebhook}},
	}
	getConfig, _ := newConfigHolder(current)

	var gotOld, gotNew *config.Config
	loop := newReconcileLoop(
		getConfig,
		applied,
		func(_ context.Context, oldConfig, newConfig *config.Config) { gotOld, gotNew = oldConfig, newConfig },
		nil,
		slog.Default(),
	)

	loop.pass(context.Background())

	if gotOld != applied || gotNew != current {
		t.Error("a hosts+repos config with zero projects must reconcile (tear down all)")
	}
	if loop.applied != current {
		t.Error("applied baseline should advance to the no-projects config")
	}
}

// A change between two valid configs reconciles and advances the baseline.
func TestReconcileLoop_ReconcilesValidConfigChange(t *testing.T) {
	applied := configWith("p1")
	current := configWith("p2") // valid, different (e.g. a project removed/renamed)
	getConfig, _ := newConfigHolder(current)

	var gotOld, gotNew *config.Config
	loop := newReconcileLoop(
		getConfig,
		applied,
		func(_ context.Context, oldConfig, newConfig *config.Config) { gotOld, gotNew = oldConfig, newConfig },
		nil,
		slog.Default(),
	)

	loop.pass(context.Background())

	if gotOld != applied || gotNew != current {
		t.Error("reconcile should run with (applied, current) for a valid config change")
	}
	if loop.applied != current {
		t.Error("applied baseline should advance to the current valid config")
	}
}
