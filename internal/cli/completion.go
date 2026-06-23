package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func CompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion",
		Short: "Generate a shell completion script",
		Long: `Generate a shell completion script for bash, zsh, or fish.

The install script sets these up automatically.
Use these for manual for manual setup.`,
	}
	cmd.AddCommand(
		newCompletionSubCmd("bash",
			"deeplo completion bash | sudo tee /usr/share/bash-completion/completions/deeplo",
			func(root *cobra.Command) error { return root.GenBashCompletionV2(os.Stdout, true) }),
		newCompletionSubCmd("zsh",
			"deeplo completion zsh | sudo tee /usr/share/zsh/site-functions/_deeplo",
			func(root *cobra.Command) error { return root.GenZshCompletion(os.Stdout) }),
		newCompletionSubCmd("fish",
			"deeplo completion fish > ~/.config/fish/completions/deeplo.fish",
			func(root *cobra.Command) error { return root.GenFishCompletion(os.Stdout, true) }),
	)
	return cmd
}

func newCompletionSubCmd(shell, installCmd string, generate func(root *cobra.Command) error) *cobra.Command {
	return &cobra.Command{
		Use:   shell,
		Short: fmt.Sprintf("Generate the %s completion script", shell),
		Long: fmt.Sprintf(`Generate the %s completion script.

Install it with:
%s`, shell, installCmd),
		Args:                  cobra.NoArgs,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return generate(cmd.Root())
		},
	}
}

type shellCompletion struct {
	shell string
	dir   string
	file  string
}

var shellCompletions = []shellCompletion{
	{shell: "bash", dir: "/usr/share/bash-completion/completions", file: "/usr/share/bash-completion/completions/deeplo"},
	{shell: "zsh", dir: "/usr/share/zsh/site-functions", file: "/usr/share/zsh/site-functions/_deeplo"},
	{shell: "fish", dir: "/usr/share/fish/vendor_completions.d", file: "/usr/share/fish/vendor_completions.d/deeplo.fish"},
}

func refreshCompletions(out io.Writer, binPath string) {
	var refreshed []string
	for _, completion := range shellCompletions {
		if _, err := os.Stat(completion.dir); err != nil {
			continue // shell's completion dir not present
		}
		script, err := generateCompletion(binPath, completion.shell)
		if err != nil {
			continue
		}
		if err := installCompletionFile(script, completion.file); err != nil {
			continue
		}
		refreshed = append(refreshed, completion.shell)
	}
	if len(refreshed) > 0 {
		fmt.Fprintf(out, "  ✓ Refreshed shell completions (%s)\n", strings.Join(refreshed, " ")) //nolint:errcheck
	}
}

var generateCompletion = func(binPath, shell string) ([]byte, error) {
	return exec.Command(binPath, "completion", shell).Output()
}

var installCompletionFile = func(script []byte, dst string) error {
	staging, err := os.CreateTemp("", "deeplo-completion-")
	if err != nil {
		return err
	}
	defer os.Remove(staging.Name()) //nolint:errcheck
	if _, err := staging.Write(script); err != nil {
		staging.Close() //nolint:errcheck
		return err
	}
	if err := staging.Close(); err != nil {
		return err
	}

	args := []string{"install", "-m", "644", staging.Name(), dst}
	var command *exec.Cmd
	if os.Getuid() == 0 {
		command = exec.Command(args[0], args[1:]...)
	} else {
		if _, err := exec.LookPath("sudo"); err != nil {
			return fmt.Errorf("root privileges required to install completion; sudo not found in PATH")
		}
		command = exec.Command("sudo", args...)
	}
	command.Stderr = os.Stderr
	return command.Run()
}
