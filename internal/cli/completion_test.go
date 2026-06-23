package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// test helpers

// overrideShellCompletions swaps the shellCompletions source of truth for the
// duration of the test, so tests can point at temp dirs instead of /usr/share.
func overrideShellCompletions(t *testing.T, completions []shellCompletion) {
	t.Helper()
	orig := shellCompletions
	t.Cleanup(func() { shellCompletions = orig })
	shellCompletions = completions
}

func stubGenerateCompletion(t *testing.T, fn func(binPath, shell string) ([]byte, error)) {
	t.Helper()
	orig := generateCompletion
	t.Cleanup(func() { generateCompletion = orig })
	generateCompletion = fn
}

func stubInstallCompletionFile(t *testing.T, fn func(script []byte, dst string) error) {
	t.Helper()
	orig := installCompletionFile
	t.Cleanup(func() { installCompletionFile = orig })
	installCompletionFile = fn
}

// captureStdout redirects os.Stdout while fn runs and returns what was written.
// CompletionCmd writes scripts straight to os.Stdout, so tests capture it here.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, reader)
		done <- buf.String()
	}()

	fn()
	_ = writer.Close()
	os.Stdout = orig
	return <-done
}

// CompletionCmd

// TestCompletionCmdGeneratesForValidShells verifies bash, zsh, and fish each
// produce a non-empty script with no error.
func TestCompletionCmdGeneratesForValidShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			var execErr error
			script := captureStdout(t, func() {
				root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
				root.AddCommand(CompletionCmd())
				root.SetArgs([]string{"completion", shell})
				execErr = root.Execute()
			})
			if execErr != nil {
				t.Fatalf("completion %s: unexpected error: %v", shell, execErr)
			}
			if strings.TrimSpace(script) == "" {
				t.Errorf("completion %s: expected a non-empty script", shell)
			}
		})
	}
}

// TestCompletionCmdRejectsExtraArgs verifies an extra positional after a shell
// (e.g. "completion bash zsh") is an error.
func TestCompletionCmdRejectsExtraArgs(t *testing.T) {
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(CompletionCmd())
	root.SetArgs([]string{"completion", "bash", "zsh"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err == nil {
		t.Error("expected an error for extra args, got nil")
	}
}

// TestCompletionCmdUnknownShellShowsHelp verifies an unsupported shell prints the
// help (listing the valid shells) instead of emitting a script.
func TestCompletionCmdUnknownShellShowsHelp(t *testing.T) {
	var out bytes.Buffer
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(CompletionCmd())
	root.SetArgs([]string{"completion", "powershell"})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("completion powershell: %v", err)
	}
	if !strings.Contains(out.String(), "bash") {
		t.Errorf("expected help listing the supported shells, got:\n%s", out.String())
	}
}

// TestCompletionCmdListsShells verifies the command exposes bash/zsh/fish as
// subcommands so its help renders an "available commands" listing.
func TestCompletionCmdListsShells(t *testing.T) {
	var out bytes.Buffer
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(CompletionCmd())
	root.SetArgs([]string{"completion", "--help"})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("completion --help: %v", err)
	}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		if !strings.Contains(out.String(), shell) {
			t.Errorf("completion --help should list %q; got:\n%s", shell, out.String())
		}
	}
}

// Registering our own "completion" command stops cobra from adding its
// powershell-bearing default, leaving exactly one completion command.
func TestCompletionCmdSuppressesCobraDefault(t *testing.T) {
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(CompletionCmd())
	root.SetArgs([]string{"--help"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var completionCmds int
	for _, sub := range root.Commands() {
		if sub.Name() == "completion" {
			completionCmds++
		}
	}
	if completionCmds != 1 {
		t.Errorf("expected exactly one 'completion' command, got %d", completionCmds)
	}
}

// refreshCompletions

// A shell is refreshed only when its completion dir exists, the binary path is
// threaded through, and the summary lists exactly the refreshed shells.
func TestRefreshCompletionsOnlyExistingDirs(t *testing.T) {
	existingDir := t.TempDir()
	missingDir := filepath.Join(t.TempDir(), "absent") // never created

	bashFile := filepath.Join(existingDir, "deeplo")
	zshFile := filepath.Join(missingDir, "_deeplo")
	overrideShellCompletions(t, []shellCompletion{
		{shell: "bash", dir: existingDir, file: bashFile},
		{shell: "zsh", dir: missingDir, file: zshFile},
	})

	var (
		generatedShells []string
		generatedBin    []string
	)
	stubGenerateCompletion(t, func(binPath, shell string) ([]byte, error) {
		generatedShells = append(generatedShells, shell)
		generatedBin = append(generatedBin, binPath)
		return []byte("# " + shell + " script\n"), nil
	})

	var (
		installedTargets []string
		installedScripts []string
	)
	stubInstallCompletionFile(t, func(script []byte, dst string) error {
		installedTargets = append(installedTargets, dst)
		installedScripts = append(installedScripts, string(script))
		return nil
	})

	var out strings.Builder
	refreshCompletions(&out, "/usr/local/bin/deeplo")

	if !slices.Equal(generatedShells, []string{"bash"}) {
		t.Errorf("generated for %v, want [bash] (zsh dir is absent)", generatedShells)
	}
	if !slices.Equal(generatedBin, []string{"/usr/local/bin/deeplo"}) {
		t.Errorf("binary path not threaded through: got %v", generatedBin)
	}
	if !slices.Equal(installedTargets, []string{bashFile}) {
		t.Errorf("installed to %v, want [%s]", installedTargets, bashFile)
	}
	if !slices.Equal(installedScripts, []string{"# bash script\n"}) {
		t.Errorf("installed script mismatch: %v", installedScripts)
	}
	if !strings.Contains(out.String(), "Refreshed shell completions (bash)") {
		t.Errorf("summary missing or wrong: %q", out.String())
	}
}

// TestRefreshCompletionsSkipsOnGenerateError verifies a shell whose script fails
// to generate is not installed and not reported.
func TestRefreshCompletionsSkipsOnGenerateError(t *testing.T) {
	dir := t.TempDir()
	overrideShellCompletions(t, []shellCompletion{
		{shell: "bash", dir: dir, file: filepath.Join(dir, "deeplo")},
	})
	stubGenerateCompletion(t, func(string, string) ([]byte, error) {
		return nil, errors.New("generation failed")
	})
	installCalled := false
	stubInstallCompletionFile(t, func([]byte, string) error {
		installCalled = true
		return nil
	})

	var out strings.Builder
	refreshCompletions(&out, "deeplo")

	if installCalled {
		t.Error("install must not run when generation fails")
	}
	if out.String() != "" {
		t.Errorf("expected no summary on failure, got %q", out.String())
	}
}

// TestRefreshCompletionsSkipsOnInstallError verifies an install failure is
// swallowed (best-effort) and the shell is left out of the summary.
func TestRefreshCompletionsSkipsOnInstallError(t *testing.T) {
	dir := t.TempDir()
	overrideShellCompletions(t, []shellCompletion{
		{shell: "bash", dir: dir, file: filepath.Join(dir, "deeplo")},
	})
	stubGenerateCompletion(t, func(string, string) ([]byte, error) {
		return []byte("script"), nil
	})
	stubInstallCompletionFile(t, func([]byte, string) error {
		return errors.New("permission denied")
	})

	var out strings.Builder
	refreshCompletions(&out, "deeplo") // must not panic or error

	if out.String() != "" {
		t.Errorf("expected no summary when install fails, got %q", out.String())
	}
}

// TestRefreshCompletionsNoDirsNoOp verifies that when no shell directory exists,
// nothing is generated and nothing is printed.
func TestRefreshCompletionsNoDirsNoOp(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	overrideShellCompletions(t, []shellCompletion{
		{shell: "bash", dir: missing, file: filepath.Join(missing, "deeplo")},
	})
	stubGenerateCompletion(t, func(string, string) ([]byte, error) {
		t.Error("generateCompletion should not be called when no dir exists")
		return nil, nil
	})
	stubInstallCompletionFile(t, func([]byte, string) error {
		t.Error("installCompletionFile should not be called when no dir exists")
		return nil
	})

	var out strings.Builder
	refreshCompletions(&out, "deeplo")

	if out.String() != "" {
		t.Errorf("expected no output, got %q", out.String())
	}
}

// removal

// TestUninstallDeletesCompletions verifies "deeplo uninstall" deletes completion files
// that exist (regardless of --purge) and never touches one that is absent.
func TestUninstallDeletesCompletions(t *testing.T) {
	overrideNative(t, nil)
	overrideSystemctlExitZero(t, false) // no active/enabled/failed unit

	origPriv := runSystemctlPrivileged
	t.Cleanup(func() { runSystemctlPrivileged = origPriv })
	runSystemctlPrivileged = func(...string) error { return nil }

	existingDir := t.TempDir()
	existingFile := filepath.Join(existingDir, "deeplo")
	if err := os.WriteFile(existingFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	missingDir := t.TempDir()
	missingFile := filepath.Join(missingDir, "_deeplo") // not created

	overrideShellCompletions(t, []shellCompletion{
		{shell: "bash", dir: existingDir, file: existingFile},
		{shell: "zsh", dir: missingDir, file: missingFile},
	})

	var removed []string
	origRemove := removeFilePrivileged
	t.Cleanup(func() { removeFilePrivileged = origRemove })
	removeFilePrivileged = func(path string) error {
		removed = append(removed, path)
		return nil
	}

	var out strings.Builder
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(&out)
	root.SetIn(strings.NewReader("n\n"))
	root.AddCommand(UninstallCmd())
	root.SetArgs([]string{"uninstall"})
	if err := root.Execute(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	if !slices.Contains(removed, existingFile) {
		t.Errorf("expected %s to be removed, got %v", existingFile, removed)
	}
	if slices.Contains(removed, missingFile) {
		t.Errorf("absent completion %s must not be removed; removed %v", missingFile, removed)
	}
	if !strings.Contains(out.String(), existingFile) {
		t.Errorf("output should report the removed completion file, got %q", out.String())
	}
}
