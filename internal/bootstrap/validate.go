package bootstrap

import (
	"fmt"
	"strings"
)

type ValidationError struct {
	Field   string
	Message string
}

func (env *Config) Validate() []ValidationError {
	var errs []ValidationError

	add := func(field, format string, args ...any) {
		errs = append(errs, ValidationError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if env.DataPath == "" {
		add("DEEPLO_DATA_DIR", "required")
	}
	if env.SSHPrivateKeyFile == "" {
		add("DEEPLO_SSH_PRIVATE_KEY_FILE", "required")
	}
	if env.SSHPort < 1 || env.SSHPort > 65535 {
		add("DEEPLO_SSH_PORT", "must be 1–65535, got %d", env.SSHPort)
	}
	if env.HTTPPort != 0 && (env.HTTPPort < 1 || env.HTTPPort > 65535) {
		add("DEEPLO_HTTP_PORT", "must be 1–65535 when set, got %d", env.HTTPPort)
	}
	if env.LogServerPort != 0 && (env.LogServerPort < 1 || env.LogServerPort > 65535) {
		add("DEEPLO_LOG_SERVER_PORT", "must be 1–65535 when set, got %d", env.LogServerPort)
	}
	if env.UnixSocket != "" && !strings.HasSuffix(env.UnixSocket, ".sock") {
		add("DEEPLO_UNIX_SOCKET", "must be a Unix socket path ending in .sock")
	}

	switch env.SSHHostKeyPolicy {
	case "strict", "accept-new":
		// valid
	default:
		add("DEEPLO_SSH_HOST_KEY_POLICY", "unknown value %q, must be 'strict' or 'accept-new'", env.SSHHostKeyPolicy)
	}
	if env.SSHHostKeyPolicy == "strict" && env.SSHKnownHosts == "" {
		add("DEEPLO_SSH_KNOWN_HOSTS", "required when DEEPLO_SSH_HOST_KEY_POLICY=strict")
	}

	if env.Source == SourceGit {
		if env.ConfigRepoURL == "" {
			add("DEEPLO_CONFIG_REPO_URL", "required in git mode")
		}
		switch env.ConfigRepoMode {
		case "", "poll":
			// valid
		case "webhook", "hybrid":
			if env.GitHubWebhookSecretFile == "" {
				add("DEEPLO_GITHUB_WEBHOOK_SECRET_FILE", "required when DEEPLO_CONFIG_REPO_MODE=%s", env.ConfigRepoMode)
			}
		default:
			add("DEEPLO_CONFIG_REPO_MODE", "unknown value %q, must be 'poll', 'webhook', or 'hybrid'", env.ConfigRepoMode)
		}
	}

	return errs
}
