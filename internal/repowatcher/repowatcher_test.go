package repowatcher_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jancernik/deeplo/internal/repowatcher"
)

const (
	testURL    = "git@github.com:owner/repo.git"
	testBranch = "main"
)

func fixedSHA(sha string) repowatcher.RemoteShaFunc {
	return func(_ context.Context, _, _ string, _ []string) (string, error) {
		return sha, nil
	}
}

func TestWatcher_SingleSubscription_HandlerCalledOnStart(t *testing.T) {
	called := make(chan string, 1)
	sub := repowatcher.Subscription{
		URL:      testURL,
		Branch:   testBranch,
		Interval: time.Hour, // long - only the immediate tick fires
		Handler:  func(_ context.Context, sha string) { called <- sha },
	}
	watcher := repowatcher.New([]repowatcher.Subscription{sub}, fixedSHA("abc123"), nil, slog.Default())
	watcher.Start(context.Background())
	defer watcher.Stop()

	select {
	case sha := <-called:
		if sha != "abc123" {
			t.Errorf("sha = %q, want abc123", sha)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called within 2s")
	}
}

func TestWatcher_Deduplication_SharedLsRemote(t *testing.T) {
	var remoteCalls atomic.Int32
	remoteSha := func(_ context.Context, _, _ string, _ []string) (string, error) {
		remoteCalls.Add(1)
		return "sha", nil
	}
	handlerA := make(chan struct{}, 1)
	handlerB := make(chan struct{}, 1)
	subs := []repowatcher.Subscription{
		{URL: testURL, Branch: testBranch, Interval: time.Hour,
			Handler: func(_ context.Context, _ string) { handlerA <- struct{}{} }},
		{URL: testURL, Branch: testBranch, Interval: time.Hour,
			Handler: func(_ context.Context, _ string) { handlerB <- struct{}{} }},
	}
	watcher := repowatcher.New(subs, remoteSha, nil, slog.Default())
	watcher.Start(context.Background())
	defer watcher.Stop()

	// Wait for both handlers to fire (first immediate tick).
	for _, ch := range []chan struct{}{handlerA, handlerB} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("handler not called within 2s")
		}
	}

	// Both subscriptions share one goroutine - exactly one ls-remote per tick.
	if n := remoteCalls.Load(); n != 1 {
		t.Errorf("git ls-remote called %d times, want 1 (two subs share one goroutine)", n)
	}
}

func TestWatcher_Deduplication_BothHandlersCalled(t *testing.T) {
	results := make(chan string, 2)
	subs := []repowatcher.Subscription{
		{URL: testURL, Branch: testBranch, Interval: time.Hour,
			Handler: func(_ context.Context, sha string) { results <- "A:" + sha }},
		{URL: testURL, Branch: testBranch, Interval: time.Hour,
			Handler: func(_ context.Context, sha string) { results <- "B:" + sha }},
	}
	watcher := repowatcher.New(subs, fixedSHA("deadbeef"), nil, slog.Default())
	watcher.Start(context.Background())
	defer watcher.Stop()

	seen := map[string]bool{}
	for range 2 {
		select {
		case result := <-results:
			seen[result] = true
		case <-time.After(2 * time.Second):
			t.Fatal("not all handlers called within 2s")
		}
	}
	if !seen["A:deadbeef"] || !seen["B:deadbeef"] {
		t.Errorf("missing handler calls: %v", seen)
	}
}

func TestWatcher_MinInterval_UsesShortestInterval(t *testing.T) {
	ticks := make(chan struct{}, 32)
	subs := []repowatcher.Subscription{
		{URL: testURL, Branch: testBranch, Interval: 25 * time.Millisecond,
			Handler: func(_ context.Context, _ string) { ticks <- struct{}{} }},
		{URL: testURL, Branch: testBranch, Interval: time.Hour, // slow subscriber
			Handler: func(_ context.Context, _ string) {}},
	}
	watcher := repowatcher.New(subs, fixedSHA("x"), nil, slog.Default())
	watcher.Start(context.Background())
	defer watcher.Stop()

	// Collect ticks over ~150ms - at 25ms we expect ≥4 ticks.
	deadline := time.After(200 * time.Millisecond)
	var count int
	for {
		select {
		case <-ticks:
			count++
		case <-deadline:
			if count < 4 {
				t.Errorf("expected ≥4 ticks at 25ms interval, got %d", count)
			}
			return
		}
	}
}

func TestWatcher_HandlerOrder_FirstRegisteredFirst(t *testing.T) {
	results := make(chan int, 2)
	subs := []repowatcher.Subscription{
		{URL: testURL, Branch: testBranch, Interval: time.Hour,
			Handler: func(_ context.Context, _ string) { results <- 1 }},
		{URL: testURL, Branch: testBranch, Interval: time.Hour,
			Handler: func(_ context.Context, _ string) { results <- 2 }},
	}
	watcher := repowatcher.New(subs, fixedSHA("x"), nil, slog.Default())
	watcher.Start(context.Background())
	defer watcher.Stop()

	first := <-results
	second := <-results
	if first != 1 || second != 2 {
		t.Errorf("handler order = [%d %d], want [1 2]", first, second)
	}
}

func TestWatcher_EmptySHA_HandlersNotCalled(t *testing.T) {
	called := make(chan struct{}, 1)
	sub := repowatcher.Subscription{
		URL:      testURL,
		Branch:   testBranch,
		Interval: time.Hour,
		Handler:  func(_ context.Context, _ string) { called <- struct{}{} },
	}
	watcher := repowatcher.New(
		[]repowatcher.Subscription{sub},
		func(_ context.Context, _, _ string, _ []string) (string, error) { return "", nil },
		nil, slog.Default(),
	)
	watcher.Start(context.Background())
	defer watcher.Stop()

	select {
	case <-called:
		t.Error("handler called for empty SHA (branch not found)")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWatcher_RemoteError_HandlersNotCalled(t *testing.T) {
	called := make(chan struct{}, 1)
	sub := repowatcher.Subscription{
		URL:      testURL,
		Branch:   testBranch,
		Interval: time.Hour,
		Handler:  func(_ context.Context, _ string) { called <- struct{}{} },
	}
	watcher := repowatcher.New(
		[]repowatcher.Subscription{sub},
		func(_ context.Context, _, _ string, _ []string) (string, error) {
			return "", errors.New("network failure")
		},
		nil, slog.Default(),
	)
	watcher.Start(context.Background())
	defer watcher.Stop()

	select {
	case <-called:
		t.Error("handler called after remote error")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWatcher_Stop_CleanShutdown(t *testing.T) {
	sub := repowatcher.Subscription{
		URL:      testURL,
		Branch:   testBranch,
		Interval: time.Hour,
		Handler:  func(_ context.Context, _ string) {},
	}
	watcher := repowatcher.New([]repowatcher.Subscription{sub}, fixedSHA("x"), nil, slog.Default())
	watcher.Start(context.Background())

	done := make(chan struct{})
	go func() {
		watcher.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s")
	}
}

func TestWatcher_NoSubscriptions_StartIsNoop(t *testing.T) {
	watcher := repowatcher.New(nil, fixedSHA("x"), nil, slog.Default())
	watcher.Start(context.Background()) // must not panic or block
	watcher.Stop()
}

func TestWatcher_ZeroInterval_UsesDefault(t *testing.T) {
	called := make(chan struct{}, 1)
	sub := repowatcher.Subscription{
		URL:    testURL,
		Branch: testBranch,
		// Interval zero → DefaultPollInterval used internally; the initial
		// immediate poll fires regardless of interval so the handler is called.
		Handler: func(_ context.Context, _ string) { called <- struct{}{} },
	}
	watcher := repowatcher.New([]repowatcher.Subscription{sub}, fixedSHA("x"), nil, slog.Default())
	watcher.Start(context.Background())
	defer watcher.Stop()

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called for zero-interval subscription")
	}
}

func TestWatcher_MinInterval_LaterSubscriberWins(t *testing.T) {
	ticks := make(chan struct{}, 32)
	subs := []repowatcher.Subscription{
		{URL: testURL, Branch: testBranch, Interval: time.Hour, // slow first
			Handler: func(_ context.Context, _ string) { ticks <- struct{}{} }},
		{URL: testURL, Branch: testBranch, Interval: 25 * time.Millisecond, // fast second
			Handler: func(_ context.Context, _ string) {}},
	}
	watcher := repowatcher.New(subs, fixedSHA("x"), nil, slog.Default())
	watcher.Start(context.Background())
	defer watcher.Stop()

	deadline := time.After(200 * time.Millisecond)
	var count int
	for {
		select {
		case <-ticks:
			count++
		case <-deadline:
			if count < 4 {
				t.Errorf("expected ≥4 ticks at 25ms interval, got %d (later sub should set minimum)", count)
			}
			return
		}
	}
}

func TestWatcher_StopBeforeStart_IsNoop(t *testing.T) {
	watcher := repowatcher.New([]repowatcher.Subscription{{
		URL: testURL, Branch: testBranch, Interval: time.Hour,
		Handler: func(_ context.Context, _ string) {},
	}}, fixedSHA("x"), nil, slog.Default())
	watcher.Stop() // must not panic or block
}

func TestWatcher_StopIdempotent(t *testing.T) {
	watcher := repowatcher.New([]repowatcher.Subscription{{
		URL: testURL, Branch: testBranch, Interval: time.Hour,
		Handler: func(_ context.Context, _ string) {},
	}}, fixedSHA("x"), nil, slog.Default())
	watcher.Start(context.Background())
	watcher.Stop()
	watcher.Stop() // second Stop must not panic or block
}

// The context passed to Start is the one forwarded to handlers, not the
// watcher's internal context.
func TestWatcher_HandlerReceivesAppContext(t *testing.T) {
	type ctxKey struct{}
	appCtx := context.WithValue(context.Background(), ctxKey{}, "marker")

	received := make(chan context.Context, 1)
	sub := repowatcher.Subscription{
		URL:      testURL,
		Branch:   testBranch,
		Interval: time.Hour,
		Handler:  func(ctx context.Context, _ string) { received <- ctx },
	}
	watcher := repowatcher.New([]repowatcher.Subscription{sub}, fixedSHA("x"), nil, slog.Default())
	watcher.Start(appCtx)
	defer watcher.Stop()

	select {
	case ctx := <-received:
		if ctx.Value(ctxKey{}) != "marker" {
			t.Error("handler did not receive the app context passed to Start()")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called")
	}
}

// Stop() must not cancel the handler context: handlers are scoped to the app
// lifetime, so in-flight handlers on a replaced watcher can still submit work.
func TestWatcher_Stop_DoesNotCancelHandlerContext(t *testing.T) {
	handlerStarted := make(chan struct{})
	ctxCancelledByStop := make(chan struct{})
	handlerUnblock := make(chan struct{})

	sub := repowatcher.Subscription{
		URL:      testURL,
		Branch:   testBranch,
		Interval: time.Hour,
		Handler: func(ctx context.Context, _ string) {
			close(handlerStarted)
			select {
			case <-ctx.Done():
				close(ctxCancelledByStop)
			case <-handlerUnblock:
			}
		},
	}
	watcher := repowatcher.New([]repowatcher.Subscription{sub}, fixedSHA("x"), nil, slog.Default())
	watcher.Start(context.Background())

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	// Stop the watcher while the handler is blocked.
	stopDone := make(chan struct{})
	go func() {
		watcher.Stop()
		close(stopDone)
	}()

	// The handler's appCtx should NOT be cancelled by Stop().
	select {
	case <-ctxCancelledByStop:
		t.Error("handler ctx was cancelled by watcher.Stop() - handlers must use app context")
	case <-time.After(100 * time.Millisecond):
	}

	// Unblock the handler so Stop() can return.
	close(handlerUnblock)
	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return after handler completed")
	}
}

func TestWatcher_TwoDistinctRepos_IndependentGroups(t *testing.T) {
	urlA := "git@github.com:owner/repo-a.git"
	urlB := "git@github.com:owner/repo-b.git"

	var remoteCalls atomic.Int32
	remoteSha := func(_ context.Context, url, _ string, _ []string) (string, error) {
		remoteCalls.Add(1)
		if url == urlA {
			return "sha-a", nil
		}
		return "sha-b", nil
	}

	results := make(chan string, 4)
	subs := []repowatcher.Subscription{
		{URL: urlA, Branch: "main", Interval: time.Hour,
			Handler: func(_ context.Context, sha string) { results <- "A:" + sha }},
		{URL: urlB, Branch: "main", Interval: time.Hour,
			Handler: func(_ context.Context, sha string) { results <- "B:" + sha }},
	}
	watcher := repowatcher.New(subs, remoteSha, nil, slog.Default())
	watcher.Start(context.Background())
	defer watcher.Stop()

	seen := map[string]bool{}
	for range 2 {
		select {
		case result := <-results:
			seen[result] = true
		case <-time.After(2 * time.Second):
			t.Fatal("not all handlers called within 2s")
		}
	}
	if !seen["A:sha-a"] || !seen["B:sha-b"] {
		t.Errorf("missing results: %v", seen)
	}
	// Two distinct repos → two separate ls-remote calls.
	if n := remoteCalls.Load(); n != 2 {
		t.Errorf("git ls-remote called %d times, want 2 (one per repo)", n)
	}
}

// cancellation logging

// warnRecorder is a slog.Handler that counts WARN (and higher) records.
type warnRecorder struct{ warnings atomic.Int32 }

func (recorder *warnRecorder) Enabled(_ context.Context, level slog.Level) bool { return true }
func (recorder *warnRecorder) Handle(_ context.Context, record slog.Record) error {
	if record.Level >= slog.LevelWarn {
		recorder.warnings.Add(1)
	}
	return nil
}
func (recorder *warnRecorder) WithAttrs(_ []slog.Attr) slog.Handler { return recorder }
func (recorder *warnRecorder) WithGroup(_ string) slog.Handler      { return recorder }

// A ls-remote error caused by context cancellation (shutdown / reload restart)
// must not be logged as a warning - it is expected teardown.
func TestWatcher_LsRemoteCancellation_NotWarned(t *testing.T) {
	recorder := &warnRecorder{}
	sub := repowatcher.Subscription{
		URL:      testURL,
		Branch:   testBranch,
		Interval: time.Hour,
		Handler:  func(_ context.Context, _ string) {},
	}
	// remoteSha blocks until cancelled, then returns git's "signal: killed"-style error.
	watcher := repowatcher.New(
		[]repowatcher.Subscription{sub},
		func(ctx context.Context, _, _ string, _ []string) (string, error) {
			<-ctx.Done()
			return "", errors.New("signal: killed")
		},
		nil, slog.New(recorder),
	)
	watcher.Start(context.Background())
	watcher.Stop() // cancels the watcher context, unblocking remoteSha with an error

	if n := recorder.warnings.Load(); n != 0 {
		t.Errorf("cancellation should not warn, got %d warnings", n)
	}
}

// A genuine ls-remote error (context still live) must still be warned.
func TestWatcher_LsRemoteError_Warned(t *testing.T) {
	recorder := &warnRecorder{}
	warned := make(chan struct{}, 1)
	sub := repowatcher.Subscription{
		URL:      testURL,
		Branch:   testBranch,
		Interval: time.Hour,
		Handler:  func(_ context.Context, _ string) {},
	}
	watcher := repowatcher.New(
		[]repowatcher.Subscription{sub},
		func(_ context.Context, _, _ string, _ []string) (string, error) {
			defer func() { warned <- struct{}{} }()
			return "", errors.New("network failure")
		},
		nil, slog.New(recorder),
	)
	watcher.Start(context.Background())
	defer watcher.Stop()

	<-warned
	// Allow the warn log to be emitted after remoteSha returns.
	time.Sleep(20 * time.Millisecond)
	if n := recorder.warnings.Load(); n != 1 {
		t.Errorf("genuine error should warn once, got %d warnings", n)
	}
}
