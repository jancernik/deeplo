package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jancernik/deeplo/internal/api"
)

func TestProbeReportsReachableHosts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/probe", serveJSON(api.ProbeResponse{
		Hosts: []api.ProbeHost{
			{Host: "web-1", Address: "10.0.0.10", OK: true},
			{Host: "web-2", Address: "10.0.0.20", OK: true},
		},
	}))
	withAdminSocket(t, startFakeDaemon(t, mux))

	out, _, err := runCmd(t, ProbeCmd(), []string{"probe"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"PROBE OK", "web-1", "10.0.0.10", "web-2", "10.0.0.20"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestProbeFailsWhenHostUnreachable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/probe", serveJSON(api.ProbeResponse{
		Hosts: []api.ProbeHost{
			{Host: "web-1", Address: "10.0.0.10", OK: true},
			{Host: "web-2", Address: "10.0.0.20", Error: "dial timeout"},
		},
	}))
	withAdminSocket(t, startFakeDaemon(t, mux))

	out, _, err := runCmd(t, ProbeCmd(), []string{"probe"})
	if err == nil {
		t.Fatalf("expected an error when a host is unreachable, output:\n%s", out)
	}
	if !strings.Contains(out, "PROBE FAIL") || !strings.Contains(out, "dial timeout") {
		t.Errorf("expected failure line for web-2, got:\n%s", out)
	}
}

func TestProbeNoHosts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/probe", serveJSON(api.ProbeResponse{}))
	withAdminSocket(t, startFakeDaemon(t, mux))

	out, _, err := runCmd(t, ProbeCmd(), []string{"probe"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No hosts configured") {
		t.Errorf("expected the no-hosts notice, got:\n%s", out)
	}
}

func TestProbeDaemonUnreachable(t *testing.T) {
	withAdminSocket(t, "/nonexistent.sock")
	_, _, err := runCmd(t, ProbeCmd(), []string{"probe"})
	if err == nil {
		t.Fatal("expected error when daemon not reachable, got nil")
	}
}
