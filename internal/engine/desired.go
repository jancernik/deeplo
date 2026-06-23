package engine

import (
	"path"
	"slices"

	"github.com/jancernik/deeplo/internal/config"
)

// Reports whether deployConfig still declares project as a deployment to host.
func TargetDesired(deployConfig *config.Config, project, host string) bool {
	_, ok := desiredRemoteDir(deployConfig, project, host)
	return ok
}

// Returns the remote directory config would deploy project to on host, and
// whether that pair is still configured. Computed identically to the deploy path
// so a teardown can tell a stale removal (same path) from a deploy_subdir rename
// (different path).
func desiredRemoteDir(deployConfig *config.Config, project, host string) (string, bool) {
	if deployConfig == nil {
		return "", false
	}

	var proj *config.Project
	for index := range deployConfig.Projects {
		if deployConfig.Projects[index].Name == project {
			proj = &deployConfig.Projects[index]
			break
		}
	}
	if proj == nil {
		return "", false
	}

	if !slices.Contains(proj.Targets, host) {
		return "", false
	}

	for index := range deployConfig.Hosts {
		if deployConfig.Hosts[index].Name == host {
			return path.Join(deployConfig.Hosts[index].DeployDir, proj.DeploySubdir), true
		}
	}
	return "", false
}
