package runlog

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jancernik/deeplo/internal/state"
)

// Returns an http.Handler that serves run log files from dir.
// Responses are text/plain; charset=utf-8.
func Handler(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Expect path: /runs/{id}/logs
		rest := strings.TrimPrefix(r.URL.Path, "/runs/")
		if rest == r.URL.Path { // prefix not present
			http.NotFound(w, r)
			return
		}
		id, suffix, ok := strings.Cut(rest, "/")
		if !ok || suffix != "logs" {
			http.NotFound(w, r)
			return
		}
		if !state.ValidRunID.MatchString(id) {
			http.NotFound(w, r)
			return
		}

		logPath := filepath.Join(dir, id+".log")
		f, err := os.Open(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer func() {
			if err := f.Close(); err != nil {
				slog.Warn("close log file", "err", err)
			}
		}()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodHead {
			return
		}
		io.Copy(w, f) //nolint:errcheck
	})
}
