package cli

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var timeNow = time.Now

func newTabWriter(out io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
}

func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func formatRelativeTime(when time.Time) string {
	if when.IsZero() {
		return "-"
	}
	elapsed := timeNow().Sub(when)
	switch {
	case elapsed < 0:
		return "just now"
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	case elapsed < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
	default:
		return when.UTC().Format("2006-01-02")
	}
}

func exactArg(name, hint string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		switch {
		case len(args) == 0:
			return fmt.Errorf("missing <%s>\n  usage: %s\n  %s", name, cmd.UseLine(), hint)
		case len(args) > 1:
			return fmt.Errorf("expected a single <%s>, got %d\n  usage: %s", name, len(args), cmd.UseLine())
		}
		return nil
	}
}

// Signals that a command has already printed its own user-facing
// output and main should exit non-zero without printing anything else.
var errSilentExit = errors.New("")

func IsSilentExit(err error) bool { return errors.Is(err, errSilentExit) }
