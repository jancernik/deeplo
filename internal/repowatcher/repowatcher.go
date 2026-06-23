// Package repowatcher polls remote git repositories and dispatches to subscribers
// when a new SHA is detected.
//
// Multiple subscriptions for the same url-branch pair share one polling
// goroutine, running at the minimum of their requested intervals. Handlers
// for a group are called sequentially in registration order.
//
//	watcher := repowatcher.New([]repowatcher.Subscription{
//	    {
//	        URL:      "git@github.com:org/repo.git",
//	        Branch:   "main",
//	        Interval: 30 * time.Second,
//	        Handler:  func(ctx context.Context, sha string) { fmt.Println("new commit", sha) },
//	    },
//	}, remoteSha, sshEnv, logger)
//	watcher.Start(appCtx)
//	defer watcher.Stop()
package repowatcher

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const DefaultPollInterval = 60 * time.Second

type RemoteShaFunc func(ctx context.Context, url, branch string, sshEnv []string) (string, error)

type Subscription struct {
	URL      string
	Branch   string
	Interval time.Duration
	Handler  func(ctx context.Context, sha string)
}

type Watcher struct {
	subscriptions []Subscription
	remoteSha     RemoteShaFunc
	sshEnv        []string
	logger        *slog.Logger
	cancel        context.CancelFunc
	waitGroup     sync.WaitGroup
}

func New(subscriptions []Subscription, remoteSha RemoteShaFunc, sshEnv []string, logger *slog.Logger) *Watcher {
	return &Watcher{
		subscriptions: subscriptions,
		remoteSha:     remoteSha,
		sshEnv:        sshEnv,
		logger:        logger.With("component", "repowatcher"),
	}
}

func (watcher *Watcher) Start(appCtx context.Context) {
	groups := watcher.buildGroups()
	if len(groups) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	watcher.cancel = cancel
	for _, group := range groups {
		watcher.waitGroup.Add(1)
		go func(group pollGroup) {
			defer watcher.waitGroup.Done()
			watcher.runGroup(ctx, appCtx, group)
		}(group)
	}
}

func (watcher *Watcher) Stop() {
	if watcher.cancel != nil {
		watcher.cancel()
	}
	watcher.waitGroup.Wait()
}

type pollGroup struct {
	url, branch string
	interval    time.Duration
	handlers    []func(ctx context.Context, sha string)
}

func (watcher *Watcher) buildGroups() []pollGroup {
	type groupKey struct{ url, branch string }
	index := make(map[groupKey]*pollGroup)
	var order []groupKey

	for _, sub := range watcher.subscriptions {
		key := groupKey{sub.URL, sub.Branch}
		group, exists := index[key]
		if !exists {
			interval := sub.Interval
			if interval <= 0 {
				interval = DefaultPollInterval
			}
			group = &pollGroup{url: sub.URL, branch: sub.Branch, interval: interval}
			index[key] = group
			order = append(order, key)
		}
		if sub.Interval > 0 && sub.Interval < group.interval {
			group.interval = sub.Interval
		}
		group.handlers = append(group.handlers, sub.Handler)
	}

	groups := make([]pollGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, *index[key])
	}
	return groups
}

func (watcher *Watcher) runGroup(ctx context.Context, appCtx context.Context, group pollGroup) {
	watcher.logger.Info("started polling", "url", group.url, "branch", group.branch, "interval", group.interval, "subscribers", len(group.handlers))

	watcher.pollGroup(ctx, appCtx, group)

	ticker := time.NewTicker(group.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			watcher.logger.Info("stopped polling", "url", group.url, "branch", group.branch)
			return
		case <-ticker.C:
			watcher.pollGroup(ctx, appCtx, group)
		}
	}
}

func (watcher *Watcher) pollGroup(ctx context.Context, appCtx context.Context, group pollGroup) {
	sha, err := watcher.remoteSha(ctx, group.url, group.branch, watcher.sshEnv)
	if err != nil {
		if ctx.Err() != nil {
			watcher.logger.Debug("git ls-remote cancelled during shutdown", "url", group.url, "branch", group.branch)
			return
		}
		watcher.logger.Warn("git ls-remote failed", "url", group.url, "branch", group.branch, "err", err)
		return
	}
	if sha == "" {
		watcher.logger.Warn("branch not found on remote", "url", group.url, "branch", group.branch)
		return
	}
	for _, handler := range group.handlers {
		handler(appCtx, sha)
	}
}
