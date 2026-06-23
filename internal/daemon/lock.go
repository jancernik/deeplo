package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Takes an exclusive, non-blocking advisory lock on a lock file in dataPath,
// ensuring only one daemon runs per data directory. The
// returned file must stay open for the daemon's lifetime; closing it (or the
// process exiting) releases the lock. A second daemon on the same data dir fails
// fast here, before touching state, the admin socket, or the HTTP port.
func acquireSingleInstanceLock(dataPath string) (*os.File, error) {
	lockPath := filepath.Join(dataPath, "deeplo.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0640)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another deeplo daemon is already running (%s is locked)", lockPath)
		}
		return nil, fmt.Errorf("lock %s: %w", lockPath, err)
	}
	return lockFile, nil
}
