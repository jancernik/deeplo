package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestFormatRelativeTime(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	orig := timeNow
	t.Cleanup(func() { timeNow = orig })
	timeNow = func() time.Time { return now }

	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{"zero", time.Time{}, "-"},
		{"future", now.Add(time.Hour), "just now"},
		{"seconds", now.Add(-30 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5m ago"},
		{"hours", now.Add(-3 * time.Hour), "3h ago"},
		{"days", now.Add(-2 * 24 * time.Hour), "2d ago"},
		{"old", now.Add(-30 * 24 * time.Hour), "2026-05-14"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := formatRelativeTime(testCase.when); got != testCase.want {
				t.Errorf("formatRelativeTime(%v) = %q, want %q", testCase.when, got, testCase.want)
			}
		})
	}
}

func TestExactArg(t *testing.T) {
	validate := exactArg("run-id", "see 'deeplo deploys history'")
	cmd := &cobra.Command{Use: "logs <run-id>"}

	if err := validate(cmd, nil); err == nil {
		t.Error("expected error for zero args")
	} else if !strings.Contains(err.Error(), "run-id") || !strings.Contains(err.Error(), "deeplo deploys history") {
		t.Errorf("missing-arg error should name the arg and hint, got %q", err.Error())
	}

	if err := validate(cmd, []string{"a", "b"}); err == nil {
		t.Error("expected error for two args")
	}

	if err := validate(cmd, []string{"only"}); err != nil {
		t.Errorf("expected no error for exactly one arg, got %v", err)
	}
}
