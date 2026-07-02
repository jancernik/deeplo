package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
)

func AuthorizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "authorize [host...]",
		Short: "Install the deploy public key on target hosts",
		Long: `Install deeplo's deploy public key into the authorized_keys of
each target host, so the daemon can connect over SSH without a password.

It resolves the public key, SSH user, and port from your bootstrap settings and
managed config. With no arguments it authorizes every configured host.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthorize(cmd, args)
		},
	}
}

func runAuthorize(cmd *cobra.Command, hostNames []string) error {
	if err := lookupSSH(); err != nil {
		return err
	}

	bootstrapConfig := bootstrap.LoadEnv()
	if bootstrapConfig.SSHPrivateKeyFile == "" {
		return fmt.Errorf("DEEPLO_SSH_PRIVATE_KEY_FILE is not set; nothing to authorize")
	}

	deployConfig, err := config.Load(bootstrapConfig.ConfigFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	hosts, err := selectAuthorizeHosts(deployConfig, hostNames)
	if err != nil {
		return err
	}

	publicKey, err := readPublicKey(bootstrapConfig.SSHPrivateKeyFile + ".pub")
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}

	// Ask the running daemon which hosts it can already reach so we skip the password prompt for those.
	authorized := probeAuthorizedHosts(cmd)

	out := cmd.OutOrStdout()
	var failed int
	for _, host := range hosts {
		target := fmt.Sprintf("%s@%s", host.EffectiveUser(bootstrapConfig.SSHUser), host.Address)
		port := host.EffectivePort(bootstrapConfig.SSHPort)

		if authorized[host.Name] {
			fmt.Fprintf(out, "%s (%s) already authorized, skipping\n", host.Name, target) //nolint:errcheck
			continue
		}
		fmt.Fprintf(out, "Authorizing %s (%s)...\n", host.Name, target) //nolint:errcheck

		if err := installAuthorizedKey(publicKey, port, target); err != nil {
			fmt.Fprintf(out, "Failed to authorize %s: %v\n", host.Name, err) //nolint:errcheck
			failed++
			continue
		}
		fmt.Fprintf(out, "✓ Authorized %s\n", host.Name) //nolint:errcheck
	}

	if failed > 0 {
		return fmt.Errorf("%d host(s) failed to authorize", failed)
	}
	return nil
}

var probeAuthorizedHosts = func(cmd *cobra.Command) map[string]bool {
	resp, err := daemonClient().Probe(cmd.Context())
	if err != nil {
		return nil
	}
	authorized := make(map[string]bool)
	for _, host := range resp.Hosts {
		if host.OK {
			authorized[host.Host] = true
		}
	}
	return authorized
}

func selectAuthorizeHosts(deployConfig *config.Config, names []string) ([]config.Host, error) {
	if len(names) == 0 {
		if len(deployConfig.Hosts) == 0 {
			return nil, fmt.Errorf("no hosts configured")
		}
		return deployConfig.Hosts, nil
	}

	hostsByName := deployConfig.HostIndex()

	selected := make([]config.Host, 0, len(names))
	for _, name := range names {
		host, ok := hostsByName[name]
		if !ok {
			return nil, fmt.Errorf("host %q not found in config", name)
		}
		selected = append(selected, host)
	}
	return selected, nil
}

var lookupSSH = func() error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return fmt.Errorf("ssh not found in PATH; install openssh-client")
	}
	return nil
}

var readPublicKey = func(path string) ([]byte, error) {
	keyBytes, err := os.ReadFile(path)
	if err == nil {
		return keyBytes, nil
	}
	if !os.IsPermission(err) {
		return nil, err
	}
	if _, lookErr := exec.LookPath("sudo"); lookErr != nil {
		return nil, err
	}
	command := exec.Command("sudo", "cat", path)
	command.Stdin = os.Stdin
	command.Stderr = os.Stderr
	return command.Output()
}

const authorizeScript = `set -e
umask 077
cd
mkdir -p .ssh
chmod 700 .ssh
touch .ssh/authorized_keys
chmod 600 .ssh/authorized_keys
key=$(cat)
if ! grep -qxF "$key" .ssh/authorized_keys; then
  if [ -s .ssh/authorized_keys ] && [ -n "$(tail -c1 .ssh/authorized_keys)" ]; then
    printf '\n' >> .ssh/authorized_keys
  fi
  printf '%s\n' "$key" >> .ssh/authorized_keys
fi
`

var installAuthorizedKey = func(publicKey []byte, port int, target string) error {
	var args []string
	if port != 0 && port != 22 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	args = append(args, target, authorizeScript)

	command := exec.Command("ssh", args...)
	command.Stdin = bytes.NewReader(publicKey)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
