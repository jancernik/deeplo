package runner_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"log/slog"

	"github.com/jancernik/deeplo/internal/runner"
)

const waitTimeout = 5 * time.Second

// collect reads exactly n Results from ch and returns them.
func collect(t *testing.T, ch <-chan runner.Result, n int) []runner.Result {
	t.Helper()
	results := make([]runner.Result, 0, n)
	for range n {
		select {
		case r, ok := <-ch:
			if !ok {
				t.Fatalf("results channel closed after %d results (want %d)", len(results), n)
			}
			results = append(results, r)
		case <-time.After(waitTimeout):
			t.Fatalf("timeout waiting for result %d/%d", len(results)+1, n)
		}
	}
	return results
}

func waitCh(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitTimeout):
		t.Fatalf("timeout waiting for %q", label)
	}
}

// notReadyCh asserts the channel does not become readable within d.
func notReadyCh(t *testing.T, ch <-chan struct{}, d time.Duration, label string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%q became ready unexpectedly", label)
	case <-time.After(d):
	}
}

func newRunner(t *testing.T, config runner.Config) *runner.Runner {
	t.Helper()
	r := runner.New(config, slog.Default())
	r.Start()
	t.Cleanup(r.Stop)
	return r
}

// Basic job execution

func TestRunner_SingleJob(t *testing.T) {
	r := newRunner(t, runner.Config{MaxWorkers: 1})

	called := make(chan struct{})
	r.Submit(context.Background(), runner.Job{ //nolint:errcheck
		ID: "j1", Project: "app", Host: "prod",
		DeployFunc: func(ctx context.Context) error {
			close(called)
			return nil
		},
	})

	waitCh(t, called, "job called")
	res := collect(t, r.Results(), 1)
	if res[0].Err != nil {
		t.Errorf("unexpected error: %v", res[0].Err)
	}
	if res[0].Job.ID != "j1" {
		t.Errorf("job ID: got %q, want j1", res[0].Job.ID)
	}
}

func TestRunner_JobError(t *testing.T) {
	r := newRunner(t, runner.Config{MaxWorkers: 1})

	r.Submit(context.Background(), runner.Job{ //nolint:errcheck
		ID: "fail", Project: "app", Host: "prod",
		DeployFunc: func(ctx context.Context) error {
			return fmt.Errorf("deploy failed")
		},
	})

	res := collect(t, r.Results(), 1)
	if res[0].Err == nil {
		t.Error("expected error, got nil")
	}
}

func TestRunner_MultipleIndependentJobs(t *testing.T) {
	const n = 5
	r := newRunner(t, runner.Config{MaxWorkers: n, MaxHostConcurrency: n})

	var count atomic.Int32
	for i := range n {
		r.Submit(context.Background(), runner.Job{ //nolint:errcheck
			ID:      fmt.Sprintf("job%d", i),
			Project: fmt.Sprintf("proj%d", i),
			Host:    fmt.Sprintf("host%d", i),
			DeployFunc: func(ctx context.Context) error {
				count.Add(1)
				return nil
			},
		})
	}

	collect(t, r.Results(), n)
	if int(count.Load()) != n {
		t.Errorf("executed %d jobs, want %d", count.Load(), n)
	}
}

func TestRunner_OnComplete(t *testing.T) {
	r := newRunner(t, runner.Config{MaxWorkers: 1})

	done := make(chan runner.Result, 1)
	r.Submit(context.Background(), runner.Job{ //nolint:errcheck
		ID: "cb", Project: "app", Host: "prod",
		DeployFunc: func(ctx context.Context) error { return nil },
		OnComplete: func(res runner.Result) { done <- res },
	})

	select {
	case res := <-done:
		if res.Job.ID != "cb" {
			t.Errorf("OnComplete: ID = %q, want cb", res.Job.ID)
		}
	case <-time.After(waitTimeout):
		t.Fatal("OnComplete not called")
	}
}

// Jobs for the same (project, host) never run concurrently, even when the
// worker and host limits would otherwise allow it.
func TestRunner_SameProjectHostSerializes(t *testing.T) {
	const n = 3
	r := newRunner(t, runner.Config{MaxWorkers: 4, MaxHostConcurrency: 4})

	started := make(chan struct{}, n)
	release := make(chan struct{})

	ctx := context.Background()
	for i := range n {
		r.Submit(ctx, runner.Job{ //nolint:errcheck
			ID: fmt.Sprintf("j%d", i), Project: "app", Host: "prod",
			DeployFunc: func(ctx context.Context) error {
				started <- struct{}{}
				<-release
				return nil
			},
		})
	}

	select {
	case <-started:
	case <-time.After(waitTimeout):
		t.Fatal("no job started within timeout")
	}

	select {
	case <-started:
		t.Error("second job started while first still holds the project+host lock")
	case <-time.After(80 * time.Millisecond):
	}

	close(release)
	results := collect(t, r.Results(), n)
	for _, res := range results {
		if res.Err != nil {
			t.Errorf("job %s: %v", res.Job.ID, res.Err)
		}
	}
}

func TestRunner_OneProjectMultipleHostsRunInParallel(t *testing.T) {
	r := newRunner(t, runner.Config{MaxWorkers: 4, MaxHostConcurrency: 4})

	started := make(chan string, 2)
	release := make(chan struct{})

	ctx := context.Background()
	for _, host := range []string{"prod-eu", "prod-us"} {
		h := host
		r.Submit(ctx, runner.Job{ //nolint:errcheck
			ID: "app/" + h, Project: "app", Host: h,
			DeployFunc: func(ctx context.Context) error {
				started <- h
				<-release
				return nil
			},
		})
	}

	seen := make(map[string]bool)
	for range 2 {
		select {
		case h := <-started:
			seen[h] = true
		case <-time.After(waitTimeout):
			t.Fatalf("only %d/2 hosts started: %v", len(seen), seen)
		}
	}
	if !seen["prod-eu"] || !seen["prod-us"] {
		t.Errorf("expected both hosts to start, got: %v", seen)
	}

	close(release)
	collect(t, r.Results(), 2)
}

func TestRunner_UnrelatedProjectsRunInParallel(t *testing.T) {
	r := newRunner(t, runner.Config{
		MaxWorkers:         4,
		MaxHostConcurrency: 2,
	})

	started := make(chan string, 2)
	release := make(chan struct{})

	ctx := context.Background()
	for _, proj := range []string{"web", "api"} {
		p := proj
		r.Submit(ctx, runner.Job{ //nolint:errcheck
			ID: p + "/prod", Project: p, Host: "prod",
			DeployFunc: func(ctx context.Context) error {
				started <- p
				<-release
				return nil
			},
		})
	}

	seen := make(map[string]bool)
	for range 2 {
		select {
		case p := <-started:
			seen[p] = true
		case <-time.After(waitTimeout):
			t.Fatalf("only %d/2 projects started: %v", len(seen), seen)
		}
	}
	if !seen["web"] || !seen["api"] {
		t.Errorf("expected both projects to start in parallel: %v", seen)
	}

	close(release)
	collect(t, r.Results(), 2)
}

func TestRunner_HostConcurrencyLimit(t *testing.T) {
	const (
		numJobs    = 6
		maxPerHost = 2
	)
	r := newRunner(t, runner.Config{
		MaxWorkers:         numJobs,
		MaxHostConcurrency: maxPerHost,
	})

	var (
		mu            sync.Mutex
		concurrent    int
		maxConcurrent int
	)

	active := make(chan struct{}, numJobs)
	release := make(chan struct{})

	ctx := context.Background()
	for i := range numJobs {
		r.Submit(ctx, runner.Job{ //nolint:errcheck
			ID:      fmt.Sprintf("job%d", i),
			Project: fmt.Sprintf("proj%d", i), // distinct → no project lock contention
			Host:    "prod",
			DeployFunc: func(ctx context.Context) error {
				mu.Lock()
				concurrent++
				if concurrent > maxConcurrent {
					maxConcurrent = concurrent
				}
				mu.Unlock()

				active <- struct{}{}
				<-release

				mu.Lock()
				concurrent--
				mu.Unlock()
				return nil
			},
		})
	}

	for range maxPerHost {
		select {
		case <-active:
		case <-time.After(waitTimeout):
			t.Fatal("timeout waiting for active jobs")
		}
	}

	mu.Lock()
	mc := maxConcurrent
	mu.Unlock()
	if mc > maxPerHost {
		t.Errorf("concurrent = %d, want ≤ %d (host limit)", mc, maxPerHost)
	}

	close(release)
	collect(t, r.Results(), numJobs)

	mu.Lock()
	finalMax := maxConcurrent
	mu.Unlock()
	if finalMax > maxPerHost {
		t.Errorf("peak concurrent = %d, want ≤ %d", finalMax, maxPerHost)
	}
}

func TestRunner_GlobalConcurrencyLimit(t *testing.T) {
	const (
		maxWorkers = 2
		numJobs    = 5
	)
	r := newRunner(t, runner.Config{
		MaxWorkers:         maxWorkers,
		MaxHostConcurrency: numJobs, // no host cap; only global cap matters
	})

	var (
		mu            sync.Mutex
		concurrent    int
		maxConcurrent int
	)

	active := make(chan struct{}, numJobs)
	release := make(chan struct{})

	ctx := context.Background()
	for i := range numJobs {
		r.Submit(ctx, runner.Job{ //nolint:errcheck
			ID:      fmt.Sprintf("g%d", i),
			Project: fmt.Sprintf("p%d", i),
			Host:    fmt.Sprintf("h%d", i), // distinct hosts → no host cap
			DeployFunc: func(ctx context.Context) error {
				mu.Lock()
				concurrent++
				if concurrent > maxConcurrent {
					maxConcurrent = concurrent
				}
				mu.Unlock()
				active <- struct{}{}
				<-release
				mu.Lock()
				concurrent--
				mu.Unlock()
				return nil
			},
		})
	}

	for range maxWorkers {
		select {
		case <-active:
		case <-time.After(waitTimeout):
			t.Fatal("timeout waiting for active jobs")
		}
	}

	mu.Lock()
	mc := maxConcurrent
	mu.Unlock()
	if mc > maxWorkers {
		t.Errorf("concurrent = %d, want ≤ %d (global limit)", mc, maxWorkers)
	}

	close(release)
	collect(t, r.Results(), numJobs)
}

// Context cancellation

// A pre-cancelled context must never run DeployFunc; shutdown relies on this to
// avoid starting new work mid-teardown.
func TestRunner_PreCancelledJobDoesNotRun(t *testing.T) {
	r := newRunner(t, runner.Config{MaxWorkers: 4})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var ran atomic.Bool
	_ = r.Submit(ctx, runner.Job{ //nolint:errcheck
		ID: "pre-cancelled", Project: "p", Host: "h",
		DeployFunc: func(context.Context) error {
			ran.Store(true)
			return nil
		},
	})

	time.Sleep(100 * time.Millisecond)
	if ran.Load() {
		t.Error("pre-cancelled job executed its DeployFunc")
	}
}

func TestRunner_ContextCancellation(t *testing.T) {
	r := newRunner(t, runner.Config{MaxWorkers: 1})

	block := make(chan struct{})
	r.Submit(context.Background(), runner.Job{ //nolint:errcheck
		ID: "blocker", Project: "p", Host: "h",
		DeployFunc: func(ctx context.Context) error { <-block; return nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := r.Submit(ctx, runner.Job{
		ID: "cancelled", Project: "p2", Host: "h2",
		DeployFunc: func(ctx context.Context) error {
			t.Error("cancelled job should not execute")
			return nil
		},
	})
	// Submit may enqueue or reject the job; either way it must not run to completion.
	_ = err

	close(block)
	timeout := time.After(waitTimeout)
	got := 0
	for got < 2 {
		select {
		case <-r.Results():
			got++
		case <-timeout:
			// The cancelled job may have been rejected at Submit time.
			if got >= 1 {
				return
			}
			t.Fatal("timeout: didn't receive any result")
		}
	}
}

// Stop blocks until in-flight jobs finish, then closes the Results channel.
func TestRunner_StopDrainsJobs(t *testing.T) {
	const n = 4
	r := runner.New(runner.Config{MaxWorkers: n, MaxHostConcurrency: n}, slog.Default())
	r.Start()

	started := make(chan struct{}, n)
	release := make(chan struct{})
	var done atomic.Int32

	for i := range n {
		r.Submit(context.Background(), runner.Job{ //nolint:errcheck
			ID: fmt.Sprintf("s%d", i), Project: fmt.Sprintf("p%d", i),
			Host: fmt.Sprintf("h%d", i), // distinct hosts → no host-sem contention
			DeployFunc: func(ctx context.Context) error {
				started <- struct{}{}
				<-release
				done.Add(1)
				return nil
			},
		})
	}

	for range n {
		select {
		case <-started:
		case <-time.After(waitTimeout):
			t.Fatal("timeout waiting for all jobs to start")
		}
	}

	close(release)
	r.Stop()

	if int(done.Load()) != n {
		t.Errorf("done = %d, want %d", done.Load(), n)
	}

	// Drain any results buffered before stopCh fired, then expect a closed channel.
	for {
		_, ok := <-r.Results()
		if !ok {
			return
		}
	}
}

// KeyedMutex

func TestKeyedMutex_ExclusiveAccess(t *testing.T) {
	km := runner.NewKeyedMutex()

	unlock1, err := km.Lock(context.Background(), "key")
	if err != nil {
		t.Fatalf("Lock 1: %v", err)
	}

	lock2Done := make(chan struct{})
	go func() {
		unlock2, err := km.Lock(context.Background(), "key")
		if err != nil {
			close(lock2Done)
			return
		}
		unlock2()
		close(lock2Done)
	}()

	notReadyCh(t, lock2Done, 80*time.Millisecond, "lock2 while lock1 held")

	unlock1()
	waitCh(t, lock2Done, "lock2 after lock1 released")
}

func TestKeyedMutex_DifferentKeysDontBlock(t *testing.T) {
	km := runner.NewKeyedMutex()

	unlock1, _ := km.Lock(context.Background(), "key-a")
	defer unlock1()

	done := make(chan struct{})
	go func() {
		unlock2, err := km.Lock(context.Background(), "key-b")
		if err == nil {
			unlock2()
		}
		close(done)
	}()
	waitCh(t, done, "key-b lock while key-a held")
}

func TestKeyedMutex_ContextCancellation(t *testing.T) {
	km := runner.NewKeyedMutex()

	unlock1, _ := km.Lock(context.Background(), "key")
	defer unlock1()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := km.Lock(ctx, "key")
		errCh <- err
	}()

	cancel()
	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Errorf("got %v, want context.Canceled", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("Lock did not return after context cancellation")
	}
}

func TestKeyedMutex_MultipleWaiters(t *testing.T) {
	km := runner.NewKeyedMutex()

	unlock, _ := km.Lock(context.Background(), "k")

	const waiters = 5
	var order []int
	var mu sync.Mutex
	wg := sync.WaitGroup{}
	wg.Add(waiters)

	// Signal before each Lock so we can wait for all waiters to queue before releasing.
	ready := make(chan struct{}, waiters)
	for i := range waiters {
		go func() {
			ready <- struct{}{}
			u, _ := km.Lock(context.Background(), "k")
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			u()
			wg.Done()
		}()
	}

	for range waiters {
		<-ready
	}
	unlock()
	wg.Wait()

	mu.Lock()
	if len(order) != waiters {
		t.Errorf("got %d completions, want %d", len(order), waiters)
	}
	mu.Unlock()
}

func TestRunner_MaxHostConcurrencyDefaultsToOne(t *testing.T) {
	r := newRunner(t, runner.Config{MaxWorkers: 4}) // MaxHostConcurrency intentionally unset

	started := make(chan struct{}, 2)
	release := make(chan struct{})

	for i := range 2 {
		r.Submit(context.Background(), runner.Job{ //nolint:errcheck
			ID: fmt.Sprintf("j%d", i), Project: fmt.Sprintf("p%d", i), Host: "prod",
			DeployFunc: func(ctx context.Context) error {
				started <- struct{}{}
				<-release
				return nil
			},
		})
	}

	select {
	case <-started:
	case <-time.After(waitTimeout):
		t.Fatal("no job started")
	}

	select {
	case <-started:
		t.Error("second job started while host is at capacity (limit should be 1)")
	case <-time.After(80 * time.Millisecond):
	}

	close(release)
	collect(t, r.Results(), 2)
}

func TestRunner_SubmitAfterStop(t *testing.T) {
	r := runner.New(runner.Config{MaxWorkers: 1}, slog.Default())
	r.Start()
	r.Stop()

	err := r.Submit(context.Background(), runner.Job{
		ID: "late", Project: "p", Host: "h",
		DeployFunc: func(ctx context.Context) error { return nil },
	})
	if err == nil {
		t.Error("Submit after Stop: expected error, got nil")
	}
}

func TestRunner_StopIsIdempotent(t *testing.T) {
	r := runner.New(runner.Config{MaxWorkers: 1}, slog.Default())
	r.Start()
	r.Stop()
	r.Stop()
}

func TestRunner_JobDurationRecorded(t *testing.T) {
	r := newRunner(t, runner.Config{MaxWorkers: 1})

	r.Submit(context.Background(), runner.Job{ //nolint:errcheck
		ID: "job", Project: "p", Host: "h",
		DeployFunc: func(ctx context.Context) error { return nil },
	})

	res := collect(t, r.Results(), 1)
	if res[0].Duration <= 0 {
		t.Errorf("duration = %v, want > 0", res[0].Duration)
	}
}

func TestRunner_ContextCancelledWaitingForHostSemaphore(t *testing.T) {
	r := newRunner(t, runner.Config{MaxWorkers: 4, MaxHostConcurrency: 1})

	blocker := make(chan struct{})
	blockerStarted := make(chan struct{})

	r.Submit(context.Background(), runner.Job{ //nolint:errcheck
		ID: "blocker", Project: "p1", Host: "prod",
		DeployFunc: func(ctx context.Context) error {
			close(blockerStarted)
			<-blocker
			return nil
		},
	})
	waitCh(t, blockerStarted, "blocker started")

	ctx, cancel := context.WithCancel(context.Background())
	r.Submit(ctx, runner.Job{ //nolint:errcheck
		ID: "waiter", Project: "p2", Host: "prod",
		DeployFunc: func(ctx context.Context) error {
			return fmt.Errorf("waiter should not have executed")
		},
	})

	cancel() // cancel while the waiter is blocked on the host semaphore

	var waiterResult runner.Result
	close(blocker)
	results := collect(t, r.Results(), 2)
	for _, res := range results {
		if res.Job.ID == "waiter" {
			waiterResult = res
		}
	}
	if waiterResult.Err == nil {
		t.Error("waiter result: expected error, got nil")
	}
}

func TestRunner_ContextCancelledWaitingForProjectLock(t *testing.T) {
	// MaxHostConcurrency=2 so both pass the host semaphore; only the project lock serializes them.
	r := newRunner(t, runner.Config{MaxWorkers: 4, MaxHostConcurrency: 2})

	blocker := make(chan struct{})
	blockerStarted := make(chan struct{})

	r.Submit(context.Background(), runner.Job{ //nolint:errcheck
		ID: "lock-holder", Project: "app", Host: "prod",
		DeployFunc: func(ctx context.Context) error {
			close(blockerStarted)
			<-blocker
			return nil
		},
	})
	waitCh(t, blockerStarted, "lock-holder started")

	ctx, cancel := context.WithCancel(context.Background())
	r.Submit(ctx, runner.Job{ //nolint:errcheck
		ID: "lock-waiter", Project: "app", Host: "prod",
		DeployFunc: func(ctx context.Context) error {
			return fmt.Errorf("lock-waiter should not have executed")
		},
	})

	cancel()

	close(blocker)
	results := collect(t, r.Results(), 2)
	var waiterResult runner.Result
	for _, res := range results {
		if res.Job.ID == "lock-waiter" {
			waiterResult = res
		}
	}
	if waiterResult.Err == nil {
		t.Error("lock-waiter result: expected error, got nil")
	}
}
