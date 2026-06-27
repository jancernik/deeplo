// DEEPLO_CONFIG_FILE                    local config file path                     default: /etc/deeplo/config.yml
// DEEPLO_CONFIG_WATCH                   reload on file change (true/false)         default: false
// DEEPLO_CONFIG_REPO_URL                git URL for config; enables git mode       optional
// DEEPLO_CONFIG_REPO_BRANCH             branch to track in git mode                default: main
// DEEPLO_CONFIG_REPO_FILE               config file path within the repo           default: config.yml
// DEEPLO_CONFIG_REPO_MODE               change detection (poll/webhook/hybrid)     default: poll
// DEEPLO_CONFIG_REPO_INTERVAL           polling interval for config repo           default: 60s

// DEEPLO_DATA_DIR                       root for state, cache, and repos           required

// DEEPLO_SSH_PRIVATE_KEY_FILE           private key for hosts and git              required
// DEEPLO_SSH_USER                       SSH username on deploy hosts               default: deploy
// DEEPLO_SSH_PORT                       SSH port                                   default: 22
// DEEPLO_SSH_KNOWN_HOSTS                known_hosts file                           default: $DEEPLO_DATA_DIR/known_hosts
// DEEPLO_SSH_HOST_KEY_POLICY            host key policy (accept-new/strict)        default: accept-new

// DEEPLO_HTTP_PORT                      HTTP listener (webhooks, health)           default: 8470
// DEEPLO_PUBLIC_URL                     public base URL (webhook + log links)      optional
// DEEPLO_UNIX_SOCKET                    admin Unix socket path                     default: /run/deeplo/deeplo.sock
// DEEPLO_ADMIN_GROUP                    group given access to the admin socket     optional (native: the operator's group)
// DEEPLO_LOG_SERVER                     serve run logs over HTTP (true/false)      default: false
// DEEPLO_LOG_SERVER_PORT                port for run logs; matches HTTP = shared   default: 8470

// DEEPLO_MAX_WORKERS                    max concurrent deploys; 0 = NumCPU         default: 0
// DEEPLO_MAX_HOST_CONCURRENCY           max concurrent deploys per host            default: 1
// DEEPLO_DEPLOY_TIMEOUT                 per-deploy timeout duration; 0 = no limit  default: 0
// DEEPLO_SHUTDOWN_GRACE                 grace for in-flight deploys on shutdown    default: 30s

// DEEPLO_LOG_LEVEL                      log verbosity (debug/info/warn/error)      default: info
// DEEPLO_LOG_FORMAT                     log output format (pretty/text/json)       default: pretty
// DEEPLO_LOG_COLOR                      colorize pretty logs (true/false)          default: true
// DEEPLO_LOG_RETENTION_DAYS             log retention in days; 0 = forever         default: 14

// DEEPLO_GITHUB_WEBHOOK_SECRET_FILE     GitHub webhook HMAC secret file            optional
// DEEPLO_GITHUB_TOKEN_FILE              GitHub PAT for deploy reporting            optional
// DEEPLO_GITHUB_ENVIRONMENT             GitHub environment name prefix             optional
// DEEPLO_GITHUB_ENVIRONMENT_HOST        env name is host/project (true/false)      default: false

// Package bootstrap reads daemon configuration from environment variables and
// manages the deploy config lifecycle.
package bootstrap

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Source string

const (
	SourceLocal Source = "local"
	SourceGit   Source = "git"
)

const DefaultUnixSocket = "/run/deeplo/deeplo.sock"

const DefaultHTTPPort = 8470

type Config struct {
	Source Source

	ConfigFile         string
	ConfigWatch        bool
	ConfigRepoURL      string
	ConfigRepoBranch   string
	ConfigRepoFile     string
	ConfigRepoMode     string
	ConfigRepoInterval time.Duration

	DataPath string

	SSHPrivateKeyFile string
	SSHUser           string
	SSHPort           int
	SSHKnownHosts     string
	SSHHostKeyPolicy  string

	HTTPPort      int
	PublicURL     string
	UnixSocket    string
	AdminGroup    string
	LogServer     bool
	LogServerPort int

	MaxWorkers         int
	MaxHostConcurrency int
	DeployTimeout      time.Duration
	ShutdownGrace      time.Duration

	LogLevel  string
	LogFormat string
	LogColor  bool

	LogRetentionDays int

	GitHubWebhookSecretFile string
	GitHubTokenFile         string
	GitHubEnvironment       string
	GitHubEnvironmentHost   bool
}

func (env *Config) LogServerEnabled() bool {
	return env.LogServer
}

func (env *Config) LogServerListenPort() int {
	if env.LogServerPort > 0 {
		return env.LogServerPort
	}
	return env.HTTPPort
}

func UnixSocketPath() string {
	if path := os.Getenv("DEEPLO_UNIX_SOCKET"); path != "" {
		return path
	}
	return DefaultUnixSocket
}

func LoadEnv() *Config { return LoadEnvFrom(os.Getenv) }

func LoadEnvFrom(getenv func(string) string) *Config {
	envString := func(key, def string) string {
		if value := getenv(key); value != "" {
			return value
		}
		return def
	}
	envBool := func(key string) bool {
		value, _ := strconv.ParseBool(getenv(key))
		return value
	}
	envBoolDefault := func(key string, defaultValue bool) bool {
		raw := getenv(key)
		if raw == "" {
			return defaultValue
		}
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return defaultValue
		}
		return value
	}
	envInt := func(key string, defaultValue int) int {
		raw := getenv(key)
		if raw == "" {
			return defaultValue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return defaultValue
		}
		return value
	}
	envDuration := func(key string, defaultValue time.Duration) time.Duration {
		raw := getenv(key)
		if raw == "" {
			return defaultValue
		}
		value, err := time.ParseDuration(raw)
		if err != nil {
			return defaultValue
		}
		return value
	}

	dataPath := envString("DEEPLO_DATA_DIR", "")

	configRepoURL := envString("DEEPLO_CONFIG_REPO_URL", "")
	source := SourceLocal
	if configRepoURL != "" {
		source = SourceGit
	}

	sshKnownHosts := envString("DEEPLO_SSH_KNOWN_HOSTS", "")
	if sshKnownHosts == "" && dataPath != "" {
		sshKnownHosts = filepath.Join(dataPath, "known_hosts")
	}

	return &Config{
		Source: source,

		ConfigFile:         envString("DEEPLO_CONFIG_FILE", "/etc/deeplo/config.yml"),
		ConfigWatch:        envBool("DEEPLO_CONFIG_WATCH"),
		ConfigRepoURL:      configRepoURL,
		ConfigRepoBranch:   envString("DEEPLO_CONFIG_REPO_BRANCH", "main"),
		ConfigRepoFile:     envString("DEEPLO_CONFIG_REPO_FILE", "config.yml"),
		ConfigRepoMode:     envString("DEEPLO_CONFIG_REPO_MODE", "poll"),
		ConfigRepoInterval: envDuration("DEEPLO_CONFIG_REPO_INTERVAL", 60*time.Second),

		DataPath: dataPath,

		SSHPrivateKeyFile: envString("DEEPLO_SSH_PRIVATE_KEY_FILE", ""),
		SSHUser:           envString("DEEPLO_SSH_USER", "deploy"),
		SSHPort:           envInt("DEEPLO_SSH_PORT", 22),
		SSHKnownHosts:     sshKnownHosts,
		SSHHostKeyPolicy:  envString("DEEPLO_SSH_HOST_KEY_POLICY", "accept-new"),

		HTTPPort:      envInt("DEEPLO_HTTP_PORT", DefaultHTTPPort),
		PublicURL:     envString("DEEPLO_PUBLIC_URL", ""),
		UnixSocket:    envString("DEEPLO_UNIX_SOCKET", DefaultUnixSocket),
		AdminGroup:    envString("DEEPLO_ADMIN_GROUP", ""),
		LogServer:     envBool("DEEPLO_LOG_SERVER"),
		LogServerPort: envInt("DEEPLO_LOG_SERVER_PORT", DefaultHTTPPort),

		MaxWorkers:         envInt("DEEPLO_MAX_WORKERS", 0),
		MaxHostConcurrency: envInt("DEEPLO_MAX_HOST_CONCURRENCY", 1),
		DeployTimeout:      envDuration("DEEPLO_DEPLOY_TIMEOUT", 0),
		ShutdownGrace:      envDuration("DEEPLO_SHUTDOWN_GRACE", 30*time.Second),

		LogLevel:  envString("DEEPLO_LOG_LEVEL", "info"),
		LogFormat: envString("DEEPLO_LOG_FORMAT", "pretty"),
		LogColor:  envBoolDefault("DEEPLO_LOG_COLOR", true),

		LogRetentionDays: envInt("DEEPLO_LOG_RETENTION_DAYS", 14),

		GitHubWebhookSecretFile: envString("DEEPLO_GITHUB_WEBHOOK_SECRET_FILE", ""),
		GitHubTokenFile:         envString("DEEPLO_GITHUB_TOKEN_FILE", ""),
		GitHubEnvironment:       envString("DEEPLO_GITHUB_ENVIRONMENT", ""),
		GitHubEnvironmentHost:   envBool("DEEPLO_GITHUB_ENVIRONMENT_HOST"),
	}
}

func ParseEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for raw := range strings.SplitSeq(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	return values, nil
}
