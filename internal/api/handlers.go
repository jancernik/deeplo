package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jancernik/deeplo/internal/state"
)

type HealthResponse struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Uptime  string `json:"uptime"`
}

type DeploymentsResponse struct {
	Deployments []*state.Deployment `json:"deployments"`
}

type RunsResponse struct {
	Runs []*state.Deployment `json:"runs"`
}

type RefreshService struct {
	Service string `json:"service"`
	State   string `json:"state"`
	Status  string `json:"status"`
}

type RefreshProject struct {
	Project  string           `json:"project"`
	Error    string           `json:"error,omitempty"`
	Services []RefreshService `json:"services"`
}

type RefreshHost struct {
	Host     string           `json:"host"`
	Error    string           `json:"error,omitempty"`
	Projects []RefreshProject `json:"projects,omitempty"`
}

type RefreshResponse struct {
	Hosts []RefreshHost `json:"hosts"`
}

type ProbeHost struct {
	Host    string `json:"host"`
	Address string `json:"address"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

type ProbeResponse struct {
	Hosts []ProbeHost `json:"hosts"`
}

type ReloadResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type DeployResponse struct {
	Targets []string `json:"targets"`
}

func (server *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		OK:      true,
		Version: server.config.Version,
		Uptime:  time.Since(server.config.StartedAt).Round(time.Second).String(),
	})
}

func (server *Server) handleDeployments(w http.ResponseWriter, _ *http.Request) {
	deployments, err := server.config.Store.ListLatestDeployments()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read deployments: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, DeploymentsResponse{Deployments: deployments})
}

func (server *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	host := r.URL.Query().Get("host")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	runs, err := server.config.Store.ListDeployments(project, host, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read runs: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, RunsResponse{Runs: runs})
}

func (server *Server) handleRunLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !state.ValidRunID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid run ID format")
		return
	}
	logPath := filepath.Join(server.config.RunsDir, id+".log")
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "log not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, "read log: "+err.Error())
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			server.config.Logger.Warn("close log file", "err", err)
		}
	}()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.Copy(w, f) //nolint:errcheck
}

func (server *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if server.config.OnRefresh == nil {
		writeError(w, http.StatusServiceUnavailable, "refresh not available")
		return
	}
	writeJSON(w, http.StatusOK, RefreshResponse{Hosts: server.config.OnRefresh(r.Context())})
}

func (server *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if server.config.OnProbe == nil {
		writeError(w, http.StatusServiceUnavailable, "probe not available")
		return
	}
	writeJSON(w, http.StatusOK, ProbeResponse{Hosts: server.config.OnProbe(r.Context())})
}

func (server *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if server.config.OnReload == nil {
		writeJSON(w, http.StatusOK, ReloadResponse{
			OK:      true,
			Message: "config reload not available in this mode",
		})
		return
	}
	if err := server.config.OnReload(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ReloadResponse{OK: true, Message: "config reloaded"})
}

func (server *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	if server.config.OnDeploy == nil {
		writeError(w, http.StatusServiceUnavailable, "deploy not available")
		return
	}
	project := r.URL.Query().Get("project")
	if project == "" {
		writeError(w, http.StatusBadRequest, "project is required")
		return
	}
	targets, err := server.config.OnDeploy(r.Context(), project, r.URL.Query().Get("host"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, DeployResponse{Targets: targets})
}

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}
