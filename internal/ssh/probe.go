package ssh

import (
	"context"
	"fmt"
	"strings"
)

const probeToken = "__deeplo_probe__"

// Runs a trivial echo command over shh to verify the connection is
// healthy and the remote shell is functional.
func Probe(ctx context.Context, connection Connection) error {
	stdout, stderr, err := connection.Run(ctx, "echo "+probeToken)
	if err != nil {
		return fmt.Errorf("probe command failed: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}
	if !strings.Contains(stdout, probeToken) {
		return fmt.Errorf("unexpected probe response: stdout=%q stderr=%q", stdout, stderr)
	}
	return nil
}
