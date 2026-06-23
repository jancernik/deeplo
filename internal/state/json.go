package state

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jancernik/deeplo/internal/utils"
)

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON for %q: %w", path, err)
	}
	return utils.AtomicWrite(path, data, 0600)
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("unmarshal %q: %w", path, err)
	}
	return nil
}
