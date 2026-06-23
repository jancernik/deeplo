package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jancernik/deeplo/internal/bootstrap"
)

func runsRegistered(mux *http.ServeMux) bool {
	_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, "/runs/abc/logs", nil))
	return pattern == "/runs/"
}

func TestNewRunLogServer(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		mux := http.NewServeMux()
		server := newRunLogServer(&bootstrap.Config{HTTPPort: 8470}, t.TempDir(), mux)
		if server != nil {
			t.Errorf("expected no dedicated server, got %q", server.Addr)
		}
		if runsRegistered(mux) {
			t.Error("/runs/ should not be registered when disabled")
		}
	})

	t.Run("enabled on the main port", func(t *testing.T) {
		mux := http.NewServeMux()
		server := newRunLogServer(&bootstrap.Config{HTTPPort: 8470, LogServer: true}, t.TempDir(), mux)
		if server != nil {
			t.Errorf("expected no dedicated server, got %q", server.Addr)
		}
		if !runsRegistered(mux) {
			t.Error("/runs/ should be registered on the main mux")
		}
	})

	t.Run("dedicated port", func(t *testing.T) {
		mux := http.NewServeMux()
		server := newRunLogServer(&bootstrap.Config{HTTPPort: 8470, LogServerPort: 9100}, t.TempDir(), mux)
		if server == nil {
			t.Fatal("expected a dedicated run-log server")
		}
		if server.Addr != ":9100" {
			t.Errorf("Addr = %q, want :9100", server.Addr)
		}
		if runsRegistered(mux) {
			t.Error("/runs/ should not be on the main mux when a dedicated port is used")
		}
	})
}
