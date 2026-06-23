package runlog

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Starts a background goroutine that deletes files older than retentionDays.
// It runs once at startup and then every 24 hours.
func StartCleanup(ctx context.Context, retentionDays int, logger *slog.Logger, dirs ...string) {
	if retentionDays <= 0 || len(dirs) == 0 {
		return
	}
	go func() {
		cleanup := func() {
			cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
			for _, dir := range dirs {
				cleanDir(dir, cutoff, retentionDays, logger)
			}
		}
		cleanup()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanup()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func cleanDir(dir string, cutoff time.Time, retentionDays int, logger *slog.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("cannot read dir", "dir", dir, "err", err)
		}
		return
	}
	var deleted int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			logger.Warn("cannot stat file", "name", e.Name(), "err", err)
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, e.Name())
			if err := os.Remove(path); err != nil {
				logger.Warn("cannot delete file", "path", path, "err", err)
			} else {
				deleted++
			}
		}
	}
	if deleted > 0 {
		logger.Info("deleted old logs", "dir", dir, "count", deleted, "retention_days", retentionDays)
	}
}
