package bootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/utils"
)

// Persists config as the baseline for startup reconciliation.
// On the next daemon start, LoadAppliedConfig reads it back to detect config
// changes that occurred while the daemon was stopped.
func SaveAppliedConfig(dataPath string, deployConfig *config.Config) error {
	if err := os.MkdirAll(filepath.Join(dataPath, "state"), 0755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.Marshal(deployConfig)
	if err != nil {
		return fmt.Errorf("marshal applied config: %w", err)
	}
	return utils.AtomicWrite(filepath.Join(dataPath, "state", "applied_config.json"), data, 0600)
}

// Reads the config that was in effect during the previous daemon run.
func LoadAppliedConfig(dataPath string) (*config.Config, error) {
	data, err := os.ReadFile(filepath.Join(dataPath, "state", "applied_config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read applied config: %w", err)
	}
	var deployConfig config.Config
	if err := json.Unmarshal(data, &deployConfig); err != nil {
		return nil, fmt.Errorf("parse applied config: %w", err)
	}
	deployConfig.ApplyDefaults()
	return &deployConfig, nil
}
