package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jancernik/deeplo/internal/build"
)

const (
	githubRepo = "jancernik/deeplo"
	installDir = "/usr/local/bin"
)

func UpdateCmd() *cobra.Command {
	var version string
	var force bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update deeplo to the latest release",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNative(); err != nil {
				return err
			}
			return runUpdate(cmd, version, force)
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "version to install (default: latest)")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already up to date")
	return cmd
}

func runUpdate(cmd *cobra.Command, version string, force bool) error {
	out := cmd.OutOrStdout()

	wasRunning := daemonReachable(adminSocket())

	if version == "" {
		fmt.Fprintln(out, "Fetching latest version...") //nolint:errcheck
		var err error
		version, err = fetchLatestVersion()
		if err != nil {
			return err
		}
	}
	// The release tag is v-prefixed; the displayed version is the bare number.
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	display := strings.TrimPrefix(version, "v")

	if !force && display == build.Version {
		fmt.Fprintf(out, "deeplo %s is already installed. Run 'deeplo update --force' to reinstall.\n", display) //nolint:errcheck
		return nil
	}

	fmt.Fprintf(out, "Updating deeplo to %s...\n", display) //nolint:errcheck

	tmpDir, err := os.MkdirTemp("", "deeplo-update-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	binPath := filepath.Join(tmpDir, "deeplo")
	if err := fetchBinary(out, version, binPath); err != nil {
		return err
	}

	installedBinary := filepath.Join(installDir, "deeplo")
	if err := installBinaryTo(binPath, installedBinary); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ Installed binary to %s/deeplo\n", installDir) //nolint:errcheck

	refreshCompletions(out, installedBinary)

	if err := runSystemctlPrivileged("daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}

	if wasRunning {
		fmt.Fprintln(out, "Restarting deeplo service...") //nolint:errcheck
		verb := "start"
		if systemctlExitZero("is-active", "--quiet", nativeUnitName) {
			verb = "restart"
		}
		if err := runSystemctlPrivileged(verb, nativeUnitName); err != nil {
			return fmt.Errorf("%s %s: %w", verb, nativeUnitName, err)
		}
		fmt.Fprintf(out, "✓ Restarted %s\n", nativeUnitName) //nolint:errcheck
	}

	fmt.Fprintf(out, "\ndeeplo updated to %s\n", display) //nolint:errcheck
	return nil
}

var fetchLatestVersion = func() (string, error) {
	resp, err := http.Get(fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)) //nolint:noctx
	if err != nil {
		return "", fmt.Errorf("fetch latest version: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch latest version: GitHub API returned HTTP %d", resp.StatusCode)
	}
	return parseLatestRelease(resp.Body)
}

func parseLatestRelease(body io.Reader) (string, error) {
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(body).Decode(&release); err != nil {
		return "", fmt.Errorf("parse GitHub API response: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("could not parse latest version from GitHub API response")
	}
	return release.TagName, nil
}

var fetchBinary = func(out io.Writer, version, dst string) error {
	arch, err := detectArch()
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/deeplo_linux_%s", githubRepo, version, arch)
	fmt.Fprintf(out, "Downloading deeplo %s (linux/%s)...\n", strings.TrimPrefix(version, "v"), arch) //nolint:errcheck

	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close() //nolint:errcheck
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return f.Close()
}

func detectArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture: %s (supported: amd64, arm64)", runtime.GOARCH)
	}
}

var installBinaryTo = func(src, dst string) error {
	args := []string{"install", "-m", "755", src, dst}
	var c *exec.Cmd
	if os.Getuid() == 0 {
		c = exec.Command(args[0], args[1:]...)
	} else {
		if _, err := exec.LookPath("sudo"); err != nil {
			return fmt.Errorf("root privileges required to install binary; sudo not found in PATH")
		}
		c = exec.Command("sudo", args...)
	}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}
