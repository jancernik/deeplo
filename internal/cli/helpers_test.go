package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runCmd executes cmd under a minimal root and returns stdout, stderr, and any error.
func runCmd(t *testing.T, cmd *cobra.Command, args []string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(cmd)
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// startFakeDaemon starts a minimal HTTP server on a temp Unix socket and
// returns the socket path. The server is shut down when the test ends.
func startFakeDaemon(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "deeplo.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sockPath
}

// serveJSON returns an HTTP handler that writes v as JSON with status 200.
func serveJSON(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// overrideNative sets checkNativeInstall for the duration of the test.
func overrideNative(t *testing.T, err error) {
	t.Helper()
	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return err }
}

// overrideSystemctlExitZero sets systemctlExitZero for the duration of the test.
func overrideSystemctlExitZero(t *testing.T, result bool) {
	t.Helper()
	orig := systemctlExitZero
	t.Cleanup(func() { systemctlExitZero = orig })
	systemctlExitZero = func(...string) bool { return result }
}

// withAdminSocket points the admin client at sock for the duration of the test.
func withAdminSocket(t *testing.T, sock string) {
	t.Helper()
	orig := adminSocket
	t.Cleanup(func() { adminSocket = orig })
	adminSocket = func() string { return sock }
}

// runManageCmd is a test helper for the update and uninstall commands. It
// overrides checkNativeInstall and captures stdout.
func runManageCmd(t *testing.T, args []string, nativeErr error) (string, error) {
	t.Helper()
	orig := checkNativeInstall
	t.Cleanup(func() { checkNativeInstall = orig })
	checkNativeInstall = func() error { return nativeErr }
	withAdminSocket(t, "/nonexistent.sock")

	var outBuf strings.Builder
	root := &cobra.Command{Use: "deeplo", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(UpdateCmd())
	root.AddCommand(UninstallCmd())
	root.SetOut(&outBuf)
	root.SetArgs(args)
	err := root.Execute()
	return outBuf.String(), err
}
