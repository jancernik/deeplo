package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

// Reads and parses a config file at path, applies defaults, and returns the result.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config file %q: %w", path, err)
	}
	return Parse(data)
}

// Parses a YAML config from raw bytes, applies defaults, and returns the result.
// Environment variable references of the form ${VAR} or ${VAR:-default} are
// expanded within scalar values.
func Parse(data []byte) (*Config, error) {
	var root yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			empty := &Config{}
			empty.ApplyDefaults()
			return empty, nil
		}
		return nil, fmt.Errorf("invalid config YAML: %w", err)
	}

	if err := expandNode(&root); err != nil {
		return nil, fmt.Errorf("env var expansion: %w", err)
	}

	var result Config
	if err := root.Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	result.ApplyDefaults()
	return &result, nil
}

func expandNode(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		expanded, err := expandString(node.Value)
		if err != nil {
			return err
		}
		if expanded != node.Value {
			node.Value = expanded
			if node.Style == 0 {
				node.Tag = ""
			}
		}
		return nil
	}
	for _, child := range node.Content {
		if err := expandNode(child); err != nil {
			return err
		}
	}
	return nil
}

var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

func expandString(s string) (string, error) {
	var missing []string
	result := envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		submatchIndexes := envVarRe.FindStringSubmatchIndex(match)
		variableName := match[submatchIndexes[2]:submatchIndexes[3]]
		hasDefault := submatchIndexes[4] >= 0
		value, present := os.LookupEnv(variableName)
		if hasDefault && value == "" {
			return match[submatchIndexes[4]:submatchIndexes[5]]
		}
		if !present {
			missing = append(missing, variableName)
			return ""
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing required env var(s): %s", strings.Join(missing, ", "))
	}
	return result, nil
}

func (config *Config) ApplyDefaults() {
	for i := range config.Repos {
		if config.Repos[i].Branch == "" {
			config.Repos[i].Branch = "main"
		}
		if config.Repos[i].TriggerMode == "" {
			config.Repos[i].TriggerMode = TriggerModePoll
		}
		if config.Repos[i].PollInterval == 0 {
			config.Repos[i].PollInterval = 60 * time.Second
		}
	}
	for i := range config.Projects {
		if config.Projects[i].DeploySubdir == "" {
			config.Projects[i].DeploySubdir = config.Projects[i].Name
		}
		if len(config.Projects[i].ComposeFiles) == 0 {
			config.Projects[i].ComposeFiles = []string{"compose.yml"}
		}
		if config.Projects[i].PersistFiles == nil {
			config.Projects[i].PersistFiles = []string{".env"}
		}
		if len(config.Projects[i].WatchPaths) == 0 && config.Projects[i].RepoSubdir != "" {
			config.Projects[i].WatchPaths = []string{config.Projects[i].RepoSubdir + "/**"}
		}
	}
}
