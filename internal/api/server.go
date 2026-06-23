package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/state"
)

type Config struct {
	SocketPath string
	StartedAt  time.Time
	Version    string
	GetConfig  func() *config.Config
	Bootstrap  *bootstrap.Config
	Store      *state.FileStore
	RunsDir    string
	OnReload   func(ctx context.Context) error
	OnRefresh  func(ctx context.Context) []RefreshHost
	OnProbe    func(ctx context.Context) []ProbeHost
	OnDeploy   func(ctx context.Context, project, host string) ([]string, error)
	Logger     *slog.Logger
}

type Server struct {
	config     Config
	httpServer *http.Server
	listener   net.Listener
}

// Wires up the admin API server. Call Start to begin accepting connections.
func New(serverConfig Config) *Server {
	server := &Server{config: serverConfig}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", server.handleHealth)
	mux.HandleFunc("GET /api/v1/deployments", server.handleDeployments)
	mux.HandleFunc("GET /api/v1/runs", server.handleRuns)
	mux.HandleFunc("GET /api/v1/runs/{id}/log", server.handleRunLog)
	mux.HandleFunc("POST /api/v1/refresh", server.handleRefresh)
	mux.HandleFunc("POST /api/v1/probe", server.handleProbe)
	mux.HandleFunc("POST /api/v1/reload", server.handleReload)
	mux.HandleFunc("POST /api/v1/deploy", server.handleDeploy)

	server.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	return server
}

// Opens the Unix socket and begins serving in a background goroutine.
func (server *Server) Start() error {
	_ = os.Remove(server.config.SocketPath)

	if err := os.MkdirAll(filepath.Dir(server.config.SocketPath), 0750); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}

	listener, err := net.Listen("unix", server.config.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", server.config.SocketPath, err)
	}
	if err := os.Chmod(server.config.SocketPath, 0660); err != nil {
		_ = listener.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	// Optionally hand the socket (and its directory) to an operator group so a
	// non-root operator can reach it without joining the daemon's own group.
	if server.config.Bootstrap != nil && server.config.Bootstrap.AdminGroup != "" {
		group := server.config.Bootstrap.AdminGroup
		if err := chownToGroup(server.config.SocketPath, group); err != nil {
			server.config.Logger.Warn("could not set admin socket group", "group", group, "err", err)
		}
	}

	server.listener = listener
	go func() {
		if err := server.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			server.config.Logger.Error("admin server error", "err", err)
		}
	}()
	server.config.Logger.Info("admin socket listening", "path", server.config.SocketPath)
	return nil
}

func chownToGroup(socketPath, groupName string) error {
	group, err := user.LookupGroup(groupName)
	if err != nil {
		return fmt.Errorf("lookup group %q: %w", groupName, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("parse gid %q: %w", group.Gid, err)
	}
	if err := os.Chown(socketPath, -1, gid); err != nil {
		return fmt.Errorf("chown socket: %w", err)
	}
	if err := os.Chown(filepath.Dir(socketPath), -1, gid); err != nil {
		return fmt.Errorf("chown socket dir: %w", err)
	}
	return nil
}

// Gracefully stops the server and removes the socket file.
func (server *Server) Shutdown(ctx context.Context) error {
	err := server.httpServer.Shutdown(ctx)
	_ = os.Remove(server.config.SocketPath)
	return err
}
