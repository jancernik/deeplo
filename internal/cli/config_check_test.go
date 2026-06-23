package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runCheck writes yaml to a temp config file, points DEEPLO_CONFIG_FILE at it,
// and runs `config check`, returning the combined output and the command error.
func runCheck(t *testing.T, yaml string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPLO_CONFIG_FILE", path)

	var out strings.Builder
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(&out)
	root.SetErr(&out)
	root.AddCommand(ConfigCmd())
	root.SetArgs([]string{"config", "check"})
	err := root.Execute()
	return out.String(), err
}

const checkValidYAML = `
hosts:
  - name: h1
    address: 10.0.0.1
    deploy_dir: /srv
repos:
  - name: r1
    url: git@github.com:o/r.git
    branch: main
    trigger_mode: webhook
projects:
  - name: p1
    repo: r1
    repo_subdir: sub
    targets:
      - h1
`

const checkNoProjectsYAML = `
hosts:
  - name: h1
    address: 10.0.0.1
    deploy_dir: /srv
repos:
  - name: r1
    url: git@github.com:o/r.git
    branch: main
    trigger_mode: webhook
`

const checkMissingHostsYAML = `
repos:
  - name: r1
    url: git@github.com:o/r.git
    branch: main
    trigger_mode: webhook
projects:
  - name: p1
    repo: r1
    repo_subdir: sub
    targets:
      - h1
`

// TestCheck_ValidConfigPasses verifies a complete config reports OK with no error.
func TestCheck_ValidConfigPasses(t *testing.T) {
	out, err := runCheck(t, checkValidYAML)
	if err != nil {
		t.Fatalf("valid config should pass, got err: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Config OK") || strings.Contains(out, "WARN") {
		t.Errorf("expected clean OK output, got:\n%s", out)
	}
}

// TestCheck_NoProjectsIsWarningNotError verifies a hosts+repos config with no
// projects passes (exit 0) but prints a warning.
func TestCheck_NoProjectsIsWarningNotError(t *testing.T) {
	out, err := runCheck(t, checkNoProjectsYAML)
	if err != nil {
		t.Fatalf("no-projects config should pass, got err: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "projects") {
		t.Errorf("expected a projects warning, got:\n%s", out)
	}
	if !strings.Contains(out, "Config OK") || !strings.Contains(out, "warning") {
		t.Errorf("expected OK-with-warning summary, got:\n%s", out)
	}
}

// TestCheck_ErrorsFail verifies a structurally broken config (missing hosts)
// fails with an error.
func TestCheck_ErrorsFail(t *testing.T) {
	out, err := runCheck(t, checkMissingHostsYAML)
	if err == nil {
		t.Fatalf("config missing hosts should fail, output:\n%s", out)
	}
	if !strings.Contains(out, "ERROR") || !strings.Contains(out, "Config INVALID") {
		t.Errorf("expected ERROR + INVALID output, got:\n%s", out)
	}
}
