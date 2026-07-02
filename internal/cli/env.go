package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/jancernik/deeplo/internal/bootstrap"
)

func EnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Work with the deeplo env file",
	}
	cmd.AddCommand(
		newEnvPathCmd(),
		newEnvEditCmd(),
		newEnvCheckCmd(),
	)
	return cmd
}

func newEnvPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path to the env file",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNative(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), nativeEnvFile) //nolint:errcheck
			return nil
		},
	}
}

func newEnvEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the env file via sudoedit",
		Long: `Open the deeplo env file via sudoedit.

sudoedit handles privilege escalation while respecting $SUDO_EDITOR / $EDITOR.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNative(); err != nil {
				return err
			}
			if _, err := os.Stat(nativeEnvFile); err != nil {
				return fmt.Errorf("env file not found: %s", nativeEnvFile)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Opening %s with sudoedit...\n", nativeEnvFile) //nolint:errcheck
			if err := runSudoEdit(nativeEnvFile); err != nil {
				return fmt.Errorf("sudoedit exited with error: %w", err)
			}
			fmt.Fprintf(out, "Edited %s\n", nativeEnvFile)                                                   //nolint:errcheck
			fmt.Fprintln(out, "Run 'deeplo env check' to validate, then 'deeplo service restart' to apply.") //nolint:errcheck
			return nil
		},
	}
}

func newEnvCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Validate the env file and referenced key/secret files",
		Long: `Parse the deeplo env file and validate the DEEPLO_* settings the same
way the daemon does at startup. It does not require the daemon to be running.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNative(); err != nil {
				return err
			}
			return runEnvCheck(cmd, nativeEnvFile)
		},
	}
}

func runEnvCheck(cmd *cobra.Command, path string) error {
	values, err := bootstrap.ParseEnvFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("env file not found: %s", path)
		}
		return fmt.Errorf("read env file: %w", err)
	}

	env := bootstrap.LoadEnvFrom(func(key string) string { return values[key] })
	out := cmd.OutOrStdout()

	problems := 0
	for _, issue := range env.Validate() {
		fmt.Fprintf(out, "ERROR  %-35s  %s\n", issue.Field, issue.Message) //nolint:errcheck
		problems++
	}

	checkFile := func(field, filePath string) {
		if filePath == "" {
			return
		}
		readable, readErr := serviceCanReadFile(filePath)
		if readErr != nil {
			fmt.Fprintf(out, "ERROR  %-35s  %v\n", field, readErr) //nolint:errcheck
			problems++
			return
		}
		if !readable {
			fmt.Fprintf(out, "ERROR  %-35s  not readable by the %s service user\n", field, nativeServiceUser) //nolint:errcheck
			problems++
		}
	}
	checkFile("DEEPLO_SSH_PRIVATE_KEY_FILE", env.SSHPrivateKeyFile)
	checkFile("DEEPLO_GITHUB_WEBHOOK_SECRET_FILE", env.GitHubWebhookSecretFile)
	checkFile("DEEPLO_GITHUB_TOKEN_FILE", env.GitHubTokenFile)
	if env.SSHHostKeyPolicy == "strict" {
		checkFile("DEEPLO_SSH_KNOWN_HOSTS", env.SSHKnownHosts)
	}

	if problems > 0 {
		fmt.Fprintf(out, "Env INVALID: %d error(s) in %s\n", problems, path) //nolint:errcheck
		return fmt.Errorf("env validation failed")
	}
	fmt.Fprintf(out, "Env OK: %s\n", path) //nolint:errcheck
	return nil
}

var serviceCanReadFile = func(path string) (bool, error) {
	file, err := os.Open(path)
	if err == nil {
		_ = file.Close()
		return true, nil
	}
	if !os.IsPermission(err) {
		return false, err
	}

	sudoPath, lookErr := exec.LookPath("sudo")
	if lookErr != nil {
		return false, err
	}
	command := exec.Command(sudoPath, "-u", nativeServiceUser, "test", "-r", path)
	command.Stdin = os.Stdin
	command.Stderr = os.Stderr
	return command.Run() == nil, nil
}
