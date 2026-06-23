package reporter

import (
	"context"
	"testing"
)

// TestNoop verifies the disabled reporter reports itself disabled and that every
// lifecycle method is a safe no-op: DeployStarted yields an empty token and the
// completion calls return no error regardless of arguments.
func TestNoop(t *testing.T) {
	noop := Noop()
	ctx := context.Background()
	info := DeployInfo{
		RepoURL:     "https://github.com/owner/repo",
		CommitSHA:   "0123456789abcdef",
		ProjectName: "web",
		HostName:    "prod",
	}

	if noop.Enabled() {
		t.Error("Noop().Enabled() = true, want false")
	}

	token := noop.DeployStarted(ctx, info, "https://logs/1")
	if token != "" {
		t.Errorf("DeployStarted token = %q, want empty", token)
	}

	if err := noop.DeploySucceeded(ctx, info, token, "ok", "https://logs/1"); err != nil {
		t.Errorf("DeploySucceeded err = %v, want nil", err)
	}
	if err := noop.DeployFailed(ctx, info, token, "boom", "https://logs/1"); err != nil {
		t.Errorf("DeployFailed err = %v, want nil", err)
	}
}
