package cli

import (
	"os"
	"strings"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:              "deeplo",
		Short:            "Agentless deployment tool for Docker Compose over SSH",
		SilenceUsage:     true,
		SilenceErrors:    true,
		PersistentPreRun: func(*cobra.Command, []string) { loadNativeEnvFile() },
	}

	root.AddGroup(&cobra.Group{ID: "inspect", Title: "INSPECT:"})
	root.AddGroup(&cobra.Group{ID: "manage", Title: "MANAGE:"})
	root.AddGroup(&cobra.Group{ID: "system", Title: "SYSTEM:"})

	add := func(cmd *cobra.Command, group string) {
		cmd.GroupID = group
		root.AddCommand(cmd)
	}

	add(DeploysCmd(), "inspect")
	add(HealthCmd(), "inspect")
	add(VersionCmd(), "inspect")

	add(AuthorizeCmd(), "manage")
	add(ConfigCmd(), "manage")
	add(DeployCmd(), "manage")
	add(ProbeCmd(), "manage")

	add(NewDaemonCmd(), "system")
	add(EnvCmd(), "system")
	add(ServiceCmd(), "system")
	add(UninstallCmd(), "system")
	add(UpdateCmd(), "system")

	configureHelp(root)

	return root
}

func loadNativeEnvFile() {
	values, err := bootstrap.ParseEnvFile(nativeEnvFile)
	if err != nil {
		return
	}
	for key, value := range values {
		if !strings.HasPrefix(key, "DEEPLO_") {
			continue
		}
		if _, set := os.LookupEnv(key); !set {
			_ = os.Setenv(key, value)
		}
	}
}

func configureHelp(root *cobra.Command) {
	root.SetUsageTemplate(rewriteUsageTemplate(root.UsageTemplate()))

	root.CompletionOptions.DisableDefaultCmd = true
	completion := CompletionCmd()
	completion.Hidden = true
	root.AddCommand(completion)

	root.InitDefaultHelpCmd()
	for _, sub := range root.Commands() {
		if sub.Name() == "help" {
			sub.Hidden = true
			sub.GroupID = "system"
		}
	}

	hideHelpFlags(root)
}

func hideHelpFlags(cmd *cobra.Command) {
	cmd.InitDefaultHelpFlag()
	if helpFlag := cmd.Flags().Lookup("help"); helpFlag != nil {
		helpFlag.Hidden = true
	}
	for _, sub := range cmd.Commands() {
		hideHelpFlags(sub)
	}
}

func rewriteUsageTemplate(template string) string {
	return strings.NewReplacer(
		"Usage:", "USAGE:",
		"Aliases:", "ALIASES:",
		"Examples:", "EXAMPLES:",
		"Available Commands:", "AVAILABLE COMMANDS:",
		"Additional Commands:", "ADDITIONAL COMMANDS:",
		"Global Flags:", "GLOBAL FLAGS:",
		"Flags:", "FLAGS:",
		"Additional help topics:", "ADDITIONAL HELP TOPICS:",
		"{{.CommandPath}} [command]{{end}}", "{{.CommandPath}} [flags] <subcommand> [command flags]{{end}}",
		"(or .IsAvailableCommand (eq .Name \"help\"))", ".IsAvailableCommand",
		`Use "{{.CommandPath}} [command] --help" for more information about a command.`,
		`For help on a subcommand, run "{{.CommandPath}} <subcommand> --help".`,
	).Replace(template)
}
