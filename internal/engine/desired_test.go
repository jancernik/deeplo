package engine_test

import (
	"testing"

	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/engine"
)

func desiredTestConfig() *config.Config {
	return &config.Config{
		Hosts: []config.Host{
			{Name: "h1", Address: "10.0.0.1", DeployDir: "/srv"},
			{Name: "h2", Address: "10.0.0.2", DeployDir: "/data"},
		},
		Projects: []config.Project{
			{Name: "app", Targets: []string{"h1", "h2"}, DeploySubdir: "app"},
		},
	}
}

func TestTargetDesired(t *testing.T) {
	deployConfig := desiredTestConfig()
	cases := []struct {
		project, host string
		want          bool
	}{
		{"app", "h1", true},
		{"app", "h2", true},
		{"app", "h3", false},   // host not a target
		{"other", "h1", false}, // project absent
	}
	for _, testCase := range cases {
		if got := engine.TargetDesired(deployConfig, testCase.project, testCase.host); got != testCase.want {
			t.Errorf("TargetDesired(%q,%q) = %v, want %v", testCase.project, testCase.host, got, testCase.want)
		}
	}
}

func TestTargetDesired_NilConfig(t *testing.T) {
	if engine.TargetDesired(nil, "app", "h1") {
		t.Error("nil config should report nothing desired")
	}
}

func TestTargetDesired_TargetedButHostUndefined(t *testing.T) {
	// Project targets a host that is not declared in hosts: not deployable.
	deployConfig := &config.Config{
		Projects: []config.Project{{Name: "app", Targets: []string{"ghost"}, DeploySubdir: "app"}},
	}
	if engine.TargetDesired(deployConfig, "app", "ghost") {
		t.Error("target referencing an undefined host should not be desired")
	}
}
