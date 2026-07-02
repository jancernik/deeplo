package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
)

func loadAndReportConfig(cmd *cobra.Command, configFile string) (*config.Config, error) {
	deployConfig, err := config.Load(configFile)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err) //nolint:errcheck
		return nil, fmt.Errorf("failed to load config")
	}

	var errs, warnings []config.ValidationIssue
	for _, issue := range deployConfig.Validate() {
		if issue.Severity == config.SeverityError {
			errs = append(errs, issue)
		} else {
			warnings = append(warnings, issue)
		}
	}

	out := cmd.OutOrStdout()
	for _, issue := range errs {
		fmt.Fprintf(out, "ERROR  %-45s  %s\n", issue.Field, issue.Message) //nolint:errcheck
	}
	for _, issue := range warnings {
		fmt.Fprintf(out, "WARN   %-45s  %s\n", issue.Field, issue.Message) //nolint:errcheck
	}

	if len(errs) > 0 {
		fmt.Fprintf(out, "Config INVALID: %d error(s) in %s\n", len(errs), configFile) //nolint:errcheck
		return nil, fmt.Errorf("config validation failed")
	}

	summary := fmt.Sprintf("Config OK: %s (%d host(s), %d repo(s), %d project(s))",
		configFile, len(deployConfig.Hosts), len(deployConfig.Repos), len(deployConfig.Projects))
	if len(warnings) > 0 {
		summary += fmt.Sprintf(", %d warning(s)", len(warnings))
	}
	fmt.Fprintln(out, summary) //nolint:errcheck
	return deployConfig, nil
}

func ConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Work with the deeplo managed config file",
	}
	cmd.AddCommand(
		newConfigPathCmd(),
		newConfigEditCmd(),
		newConfigCheckCmd(),
		newConfigReloadCmd(),
	)
	return cmd
}

func newConfigReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Ask the daemon to reload its config",
		Long: `Instruct the running daemon to re-read its managed config from disk or
its configured git repository and apply any changes immediately.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := daemonClient().Reload(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), resp.Message) //nolint:errcheck
			return nil
		},
	}
}

func newConfigCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Validate the config file and report any issues",
		Long: `Validate the managed config file and report errors and warnings.
It does not require the daemon to be running.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := loadAndReportConfig(cmd, bootstrap.LoadEnv().ConfigFile)
			return err
		},
	}
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path to the managed config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			env := bootstrap.LoadEnv()
			fmt.Fprintln(cmd.OutOrStdout(), env.ConfigFile) //nolint:errcheck
			return nil
		},
	}
}

func newConfigEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the managed config file in an editor",
		Long: `Open the managed config file in an editor.

If the file is writable by the current user it is opened with the preferred
editor ($VISUAL > $EDITOR > vi). If the file is not writable (e.g. root-owned
/etc/deeplo/config.yml on a native install) sudoedit is used so the editor
receives an editable copy.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			env := bootstrap.LoadEnv()
			path := env.ConfigFile
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("config file not found: %s\n  set DEEPLO_CONFIG_FILE or create the file", path)
			}
			out := cmd.OutOrStdout()
			var editErr error
			if isWritableFile(path) {
				editor := resolveEditor()
				fmt.Fprintf(out, "Opening %s with %s...\n", path, editor) //nolint:errcheck
				editErr = runEditor(path)
			} else {
				fmt.Fprintf(out, "Opening %s with sudoedit...\n", path) //nolint:errcheck
				editErr = runSudoEdit(path)
			}
			if editErr != nil {
				return fmt.Errorf("editor exited with error: %w", editErr)
			}
			fmt.Fprintf(out, "Edited %s\n", path)                                                             //nolint:errcheck
			fmt.Fprintln(out, "Run 'deeplo config check' to validate, then 'deeplo config reload' to apply.") //nolint:errcheck
			return nil
		},
	}
}
