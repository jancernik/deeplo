// Package runner implements a concurrent job dispatcher for deploy and teardown jobs.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const jobBuffer = 512

type Config struct {
	// The maximum number of jobs that execute simultaneously across all hosts.
	// Values ≤ 0 default to the number of available cores.
	MaxWorkers int

	// The maximum number of jobs that can execute simultaneously against a single host.
	// Values ≤ 0 default to 1.
	MaxHostConcurrency int
}

type Job struct {
	ID string

	Project string
	Host    string

	DeployFunc func(ctx context.Context) error
	OnComplete func(Result)
}

type Result struct {
	Job      Job
	Err      error
	Duration time.Duration
}

type jobEntry struct {
	job Job
	ctx context.Context
}

type Runner struct {
	config          Config
	globalSemaphore chan struct{}
	hostSemaphores  map[string]chan struct{}
	mutex           sync.Mutex
	projectLock     *KeyedMutex
	jobChannel      chan jobEntry
	results         chan Result
	waitGroup       sync.WaitGroup
	stopOnce        sync.Once
	stopChannel     chan struct{}
	stopped         atomic.Bool
	logger          *slog.Logger
}

func New(config Config, logger *slog.Logger) *Runner {
	workers := config.MaxWorkers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	return &Runner{
		config:          config,
		globalSemaphore: make(chan struct{}, workers),
		hostSemaphores:  make(map[string]chan struct{}),
		projectLock:     NewKeyedMutex(),
		jobChannel:      make(chan jobEntry, jobBuffer),
		results:         make(chan Result, jobBuffer),
		stopChannel:     make(chan struct{}),
		logger:          logger.With("component", "runner"),
	}
}

func (runner *Runner) Start() {
	runner.waitGroup.Add(1)
	go runner.dispatch()
}

func (runner *Runner) Submit(ctx context.Context, job Job) error {
	if runner.stopped.Load() {
		return fmt.Errorf("runner stopped")
	}
	select {
	case runner.jobChannel <- jobEntry{job: job, ctx: ctx}:
		return nil
	case <-runner.stopChannel:
		return fmt.Errorf("runner stopped")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (runner *Runner) Results() <-chan Result {
	return runner.results
}

func (runner *Runner) Stop() {
	runner.stopOnce.Do(func() {
		runner.stopped.Store(true)
		close(runner.stopChannel)
		runner.waitGroup.Wait()

		dropped := 0
		for {
			select {
			case <-runner.jobChannel:
				dropped++
			default:
				if dropped > 0 {
					runner.logger.Warn("dropped queued jobs on shutdown", "count", dropped)
				}
				close(runner.results)
				return
			}
		}
	})
}

func (runner *Runner) dispatch() {
	defer runner.waitGroup.Done()
	for {
		select {
		case entry := <-runner.jobChannel:
			runner.waitGroup.Add(1)
			go func() {
				defer runner.waitGroup.Done()
				runner.execute(entry)
			}()
		case <-runner.stopChannel:
			return
		}
	}
}

func (runner *Runner) execute(entry jobEntry) {
	job := entry.job
	ctx := entry.ctx

	runner.logger.Debug("job pending", "id", job.ID, "project", job.Project, "host", job.Host)

	if err := ctx.Err(); err != nil {
		runner.finish(job, err, 0)
		return
	}

	select {
	case runner.globalSemaphore <- struct{}{}:
	case <-runner.stopChannel:
		runner.finish(job, fmt.Errorf("runner stopped"), 0)
		return
	case <-ctx.Done():
		runner.finish(job, ctx.Err(), 0)
		return
	}
	defer func() { <-runner.globalSemaphore }()

	hostSemaphore := runner.getHostSemaphore(job.Host)
	select {
	case hostSemaphore <- struct{}{}:
	case <-runner.stopChannel:
		runner.finish(job, fmt.Errorf("runner stopped"), 0)
		return
	case <-ctx.Done():
		runner.finish(job, ctx.Err(), 0)
		return
	}
	defer func() { <-hostSemaphore }()

	lockKey := job.Project + "\x00" + job.Host
	unlock, err := runner.projectLock.Lock(ctx, lockKey)
	if err != nil {
		runner.finish(job, fmt.Errorf("acquire project lock: %w", err), 0)
		return
	}
	defer unlock()

	if err := ctx.Err(); err != nil {
		runner.finish(job, err, 0)
		return
	}

	runner.logger.Info("job started", "id", job.ID)
	start := time.Now()
	err = job.DeployFunc(ctx)
	duration := time.Since(start)

	if err != nil {
		runner.logger.Info("job failed", "id", job.ID, "duration", duration, "err", err)
	} else {
		runner.logger.Info("job completed", "id", job.ID, "duration", duration)
	}
	runner.finish(job, err, duration)
}

func (runner *Runner) finish(job Job, err error, duration time.Duration) {
	res := Result{Job: job, Err: err, Duration: duration}
	if job.OnComplete != nil {
		job.OnComplete(res)
	}
	select {
	case runner.results <- res:
	case <-runner.stopChannel:
	}
}

func (runner *Runner) getHostSemaphore(host string) chan struct{} {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	semaphore, ok := runner.hostSemaphores[host]
	if !ok {
		n := runner.config.MaxHostConcurrency
		if n <= 0 {
			n = 1
		}
		semaphore = make(chan struct{}, n)
		runner.hostSemaphores[host] = semaphore
	}
	return semaphore
}
