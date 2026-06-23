package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runServiceCmd executes the service command tree, stubbing checkNativeInstall
// and the systemctl/journalctl runners and capturing their invocations.
func runServiceCmd(t *testing.T, args []string, nativeErr error, captured *[]string) error {
	t.Helper()

	orig := checkNativeInstall
	origSysctl := runSystemctl
	origPriv := runSystemctlPrivileged
	origJournal := runJournalctl
	t.Cleanup(func() {
		checkNativeInstall = orig
		runSystemctl = origSysctl
		runSystemctlPrivileged = origPriv
		runJournalctl = origJournal
	})

	checkNativeInstall = func() error { return nativeErr }
	// Both variants write to the same capture slice so cases checking for
	// "restart deeplo" etc. continue to work regardless of which var is used.
	runSystemctl = func(args ...string) error {
		*captured = append(*captured, strings.Join(args, " "))
		return nil
	}
	runSystemctlPrivileged = func(args ...string) error {
		*captured = append(*captured, strings.Join(args, " "))
		return nil
	}
	runJournalctl = func(args ...string) error {
		*captured = append(*captured, strings.Join(args, " "))
		return nil
	}

	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(ServiceCmd())
	root.SetArgs(args)
	return root.Execute()
}

// TestServiceRequiresNative verifies that every service subcommand returns
// errNotNative when checkNativeInstall reports a non-native environment.
func TestServiceRequiresNative(t *testing.T) {
	subcommands := []string{"status", "start", "stop", "restart", "enable", "disable", "logs"}
	nativeErr := errors.New("not a native install")

	for _, sub := range subcommands {
		t.Run(sub, func(t *testing.T) {
			var captured []string
			err := runServiceCmd(t, []string{"service", sub}, nativeErr, &captured)
			if err == nil {
				t.Fatalf("service %s: expected error on non-native install, got nil", sub)
			}
			if len(captured) > 0 {
				t.Errorf("service %s: systemctl/journalctl should not have been called, got %v", sub, captured)
			}
		})
	}
}

// TestServiceSubcommands verifies that each service subcommand calls the
// correct systemctl verb when running on a native install.
func TestServiceSubcommands(t *testing.T) {
	cases := []struct {
		sub  string
		want string
	}{
		{"status", "status deeplo"},
		{"start", "start deeplo"},
		{"stop", "stop deeplo"},
		{"restart", "restart deeplo"},
		{"enable", "enable deeplo"},
		{"disable", "disable deeplo"},
	}

	for _, testCase := range cases {
		t.Run(testCase.sub, func(t *testing.T) {
			var captured []string
			err := runServiceCmd(t, []string{"service", testCase.sub}, nil, &captured)
			if err != nil {
				t.Fatalf("service %s: unexpected error: %v", testCase.sub, err)
			}
			if len(captured) != 1 || captured[0] != testCase.want {
				t.Errorf("service %s: got systemctl args %v, want [%q]", testCase.sub, captured, testCase.want)
			}
		})
	}
}

// TestServiceEnableNowFlag verifies that "service enable --now" passes --now to
// systemctl so the service is enabled and started in one step.
func TestServiceEnableNowFlag(t *testing.T) {
	var captured []string
	err := runServiceCmd(t, []string{"service", "enable", "--now"}, nil, &captured)
	if err != nil {
		t.Fatalf("service enable --now: unexpected error: %v", err)
	}
	if len(captured) != 1 || captured[0] != "enable --now deeplo" {
		t.Errorf("service enable --now: got systemctl args %v, want [%q]", captured, "enable --now deeplo")
	}
}

// TestServiceLogsDefaultArgs verifies that "service logs" passes the expected
// default arguments to journalctl.
func TestServiceLogsDefaultArgs(t *testing.T) {
	var captured []string
	err := runServiceCmd(t, []string{"service", "logs"}, nil, &captured)
	if err != nil {
		t.Fatalf("service logs: unexpected error: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("service logs: expected 1 journalctl call, got %d: %v", len(captured), captured)
	}
	got := captured[0]
	for _, want := range []string{"-u deeplo", "--no-pager", "-n50"} {
		if !strings.Contains(got, want) {
			t.Errorf("service logs: journalctl args %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "-f") {
		t.Errorf("service logs: journalctl args should not contain -f without --follow flag")
	}
}

// TestServiceLogsFollowFlag verifies that --follow adds -f to journalctl args.
func TestServiceLogsFollowFlag(t *testing.T) {
	var captured []string
	err := runServiceCmd(t, []string{"service", "logs", "--follow"}, nil, &captured)
	if err != nil {
		t.Fatalf("service logs --follow: unexpected error: %v", err)
	}
	if len(captured) == 0 || !strings.Contains(captured[0], "-f") {
		t.Errorf("service logs --follow: expected -f in journalctl args, got %v", captured)
	}
}

// Mutating commands (start/stop/restart/enable/disable) go through
// runSystemctlPrivileged so non-root users are prompted via sudo, not polkit.
func TestServiceMutatingCommandsUsePrivilegedVariant(t *testing.T) {
	mutating := []string{"start", "stop", "restart", "enable", "disable"}

	for _, sub := range mutating {
		t.Run(sub, func(t *testing.T) {
			orig := checkNativeInstall
			t.Cleanup(func() { checkNativeInstall = orig })
			checkNativeInstall = func() error { return nil }

			// plain runSystemctl must NOT be called for mutating commands
			origSysctl := runSystemctl
			t.Cleanup(func() { runSystemctl = origSysctl })
			runSystemctl = func(args ...string) error {
				t.Errorf("service %s: plain runSystemctl called - must use runSystemctlPrivileged instead; args: %v", sub, args)
				return nil
			}

			var privCalled bool
			origPriv := runSystemctlPrivileged
			t.Cleanup(func() { runSystemctlPrivileged = origPriv })
			runSystemctlPrivileged = func(args ...string) error {
				privCalled = true
				return nil
			}

			root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
			root.AddCommand(ServiceCmd())
			root.SetArgs([]string{"service", sub})
			_ = root.Execute()

			if !privCalled {
				t.Errorf("service %s: runSystemctlPrivileged was not called", sub)
			}
		})
	}
}

// TestServiceStatusUsesPlainSystemctl verifies that "service status" uses the
// plain (non-sudo) runSystemctl variant, which works without root.
func TestServiceStatusUsesPlainSystemctl(t *testing.T) {
	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return nil }

	var plainCalled bool
	origSysctl := runSystemctl
	t.Cleanup(func() { runSystemctl = origSysctl })
	runSystemctl = func(args ...string) error {
		plainCalled = true
		return nil
	}

	origPriv := runSystemctlPrivileged
	t.Cleanup(func() { runSystemctlPrivileged = origPriv })
	runSystemctlPrivileged = func(args ...string) error {
		t.Errorf("service status: runSystemctlPrivileged called - should use plain runSystemctl")
		return nil
	}

	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(ServiceCmd())
	root.SetArgs([]string{"service", "status"})
	_ = root.Execute()

	if !plainCalled {
		t.Errorf("service status: plain runSystemctl was not called")
	}
}
