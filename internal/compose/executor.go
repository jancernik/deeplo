package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/jancernik/deeplo/internal/ssh"
)

// Runs Docker Compose commands on a remote host through connection.
type Executor struct {
	connection ssh.Connection
	remoteDir  string // stable project directory on the remote host
	project    string // stable compose project name
	logger     *slog.Logger
}

func NewExecutor(connection ssh.Connection, remoteDir, project string, logger *slog.Logger) *Executor {
	return &Executor{
		connection: connection,
		remoteDir:  remoteDir,
		project:    sanitizeProjectName(project),
		logger:     logger.With("component", "compose"),
	}
}

// Runs `docker compose config` in dir to validate the compose files
func (executor *Executor) Preflight(ctx context.Context, dir string, composeFiles []string) error {
	command := executor.buildCommand(dir, composeFiles, "config")
	stdout, stderr, err := executor.connection.Run(ctx, command)
	if err != nil {
		return fmt.Errorf("compose config failed:\nstdout: %s\nstderr: %s", stdout, strings.TrimSpace(stderr))
	}
	return nil
}

// Stages the bundle in <remoteDir>.staging/, validates compose config, swaps
// staging into place, brings services up, and verifies all are running.
// On preflight failure staging is cleaned up, the live directory is untouched.
// The swap has a brief window where remoteDir is absent, unavoidable over SSH
func (executor *Executor) Deploy(ctx context.Context, bundle *Bundle, options DeployOptions) (*DeployResult, error) {
	if err := bundle.Validate(); err != nil {
		return nil, fmt.Errorf("invalid bundle: %w", err)
	}
	if len(options.ComposeFiles) == 0 {
		return nil, fmt.Errorf("at least one compose file is required in DeployOptions")
	}

	stagingDir := executor.remoteDir + ".staging"
	oldDir := executor.remoteDir + ".old"

	executor.cleanup(ctx, stagingDir)
	mkdirCommand := fmt.Sprintf("mkdir -p %s", shellQuote(stagingDir))
	if _, stderr, err := executor.connection.Run(ctx, mkdirCommand); err != nil {
		return nil, fmt.Errorf("create staging dir: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	for _, file := range bundle.Files {
		remotePath := path.Join(stagingDir, file.RemoteName)
		executor.logger.Debug("uploading bundle file", "file", file.RemoteName)
		if err := executor.connection.Upload(ctx, file.LocalPath, remotePath); err != nil {
			executor.cleanup(ctx, stagingDir)
			return nil, fmt.Errorf("upload %q: %w", file.RemoteName, err)
		}
	}

	// Only copy persist files absent from staging, a committed version always wins over the host copy.
	for _, name := range options.PersistFiles {
		srcPath := path.Join(executor.remoteDir, name)
		dstDir := path.Join(stagingDir, path.Dir(name))
		dstPath := path.Join(stagingDir, name)
		copyCommand := fmt.Sprintf(
			"if [ -f %s ] && [ ! -e %s ]; then mkdir -p %s && cp %s %s; fi",
			shellQuote(srcPath), shellQuote(dstPath), shellQuote(dstDir), shellQuote(srcPath), shellQuote(dstPath),
		)
		if _, stderr, err := executor.connection.Run(ctx, copyCommand); err != nil {
			executor.cleanup(ctx, stagingDir)
			return nil, fmt.Errorf("copy persist file %q: %w (stderr: %s)", name, err, strings.TrimSpace(stderr))
		}
	}

	executor.logger.Debug("running compose preflight", "dir", stagingDir)
	if err := executor.Preflight(ctx, stagingDir, options.ComposeFiles); err != nil {
		executor.cleanup(ctx, stagingDir)
		return nil, fmt.Errorf("preflight: %w", err)
	}

	executor.logger.Debug("performing directory swap", "dir", executor.remoteDir)
	swapCommand := fmt.Sprintf(
		"rm -rf %s && ( [ ! -d %s ] || mv %s %s ) && mv %s %s",
		shellQuote(oldDir),
		shellQuote(executor.remoteDir), shellQuote(executor.remoteDir), shellQuote(oldDir),
		shellQuote(stagingDir), shellQuote(executor.remoteDir),
	)
	if _, stderr, err := executor.connection.Run(ctx, swapCommand); err != nil {
		return nil, fmt.Errorf("directory swap failed: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	executor.cleanup(ctx, oldDir)

	executor.logger.Info("running docker compose up", "dir", executor.remoteDir)
	upCommand := executor.buildCommand(executor.remoteDir, options.ComposeFiles, "up", "--detach", "--remove-orphans", "--quiet-pull", "--force-recreate", "--build")
	stdout, stderr, err := executor.connection.Run(ctx, upCommand)
	composeOutput := strings.TrimSpace(stdout)
	if trimmedStderr := strings.TrimSpace(stderr); trimmedStderr != "" {
		if composeOutput != "" {
			composeOutput += "\n"
		}
		composeOutput += trimmedStderr
	}
	if err != nil {
		return nil, fmt.Errorf("docker compose up: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	executor.logger.Debug("verifying runtime state", "dir", executor.remoteDir)
	services, psErr := executor.Ps(ctx, options.ComposeFiles)
	result := &DeployResult{Services: services, ComposeOutput: composeOutput}
	if psErr != nil {
		return result, fmt.Errorf("runtime check: %w", psErr)
	}
	for _, service := range services {
		if !strings.EqualFold(service.State, "running") {
			return result, fmt.Errorf("runtime check: service %q is in state %q (expected running)", service.Service, service.State)
		}
	}

	return result, nil
}

// Runs `docker compose ps --format json` and returns the parsed service list.
func (executor *Executor) Ps(ctx context.Context, composeFiles []string) ([]ServiceStatus, error) {
	command := executor.buildCommand(executor.remoteDir, composeFiles, "ps", "--format", "json")
	stdout, stderr, err := executor.connection.Run(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("compose ps: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}
	stdout = strings.TrimSpace(stdout)
	if stdout == "" || stdout == "null" || stdout == "[]" {
		return nil, nil
	}
	var services []ServiceStatus
	decoder := json.NewDecoder(strings.NewReader(stdout))
	for decoder.More() {
		var service ServiceStatus
		if err := decoder.Decode(&service); err != nil {
			return nil, fmt.Errorf("parse compose ps output: %w\nraw: %s", err, stdout)
		}
		services = append(services, service)
	}
	return services, nil
}

// Runs `docker compose down --remove-orphans` to stop and remove containers.
func (executor *Executor) Down(ctx context.Context, composeFiles []string) error {
	command := executor.buildCommand(executor.remoteDir, composeFiles, "down", "--remove-orphans")
	_, stderr, err := executor.connection.Run(ctx, command)
	if err != nil {
		return fmt.Errorf("compose down: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}
	return nil
}

// Removes the given directory, best-effort.
func (executor *Executor) cleanup(ctx context.Context, directory string) {
	if _, _, err := executor.connection.Run(ctx, "rm -rf "+shellQuote(directory)); err != nil {
		executor.logger.Debug("directory cleanup failed (non-fatal)", "err", err)
	}
}

func (executor *Executor) buildCommand(dir string, composeFiles []string, subcommand string, args ...string) string {
	parts := []string{"cd", shellQuote(dir), "&&", "docker compose"}
	if executor.project != "" {
		parts = append(parts, "--project-name", shellQuote(executor.project))
	}
	for _, composeFile := range composeFiles {
		parts = append(parts, "-f", shellQuote(composeFile))
	}
	parts = append(parts, subcommand)
	parts = append(parts, args...)
	return strings.Join(parts, " ")
}

func sanitizeProjectName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}

	var builder strings.Builder
	lastWasReplacement := false
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
			lastWasReplacement = false
		case character == '-' || character == '_':
			builder.WriteRune(character)
			lastWasReplacement = false
		default:
			if !lastWasReplacement {
				builder.WriteByte('-')
				lastWasReplacement = true
			}
		}
	}

	out := strings.Trim(builder.String(), "-_")
	if out == "" {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "deeplo-" + out
	}
	return out
}

// Returns single-quotes argument for safe shell use.
func shellQuote(argument string) string {
	return "'" + strings.ReplaceAll(argument, "'", "'\\''") + "'"
}
