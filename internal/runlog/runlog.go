// Package runlog handles per-deployment run log files.
package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type RunLog struct {
	file  *os.File
	mutex sync.Mutex
}

// Creates a new run log file at dir/id.log.
// It creates dir if it does not exist.
func Open(dir, id string) (*RunLog, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create run log dir: %w", err)
	}
	path := filepath.Join(dir, id+".log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("open run log: %w", err)
	}
	return &RunLog{file: file}, nil
}

func (runLog *RunLog) Println(line string) {
	if runLog == nil {
		return
	}
	runLog.mutex.Lock()
	defer runLog.mutex.Unlock()
	fmt.Fprintln(runLog.file, line) //nolint:errcheck
}

func (runLog *RunLog) Logf(format string, args ...any) {
	if runLog == nil {
		return
	}
	runLog.mutex.Lock()
	defer runLog.mutex.Unlock()
	ts := time.Now().UTC().Format("15:04:05Z")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(runLog.file, "[%s] %s\n", ts, msg) //nolint:errcheck
}

func (runLog *RunLog) Close() error {
	if runLog == nil {
		return nil
	}
	return runLog.file.Close()
}
