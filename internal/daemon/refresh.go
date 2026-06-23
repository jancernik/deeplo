package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sync"
	"time"

	"github.com/jancernik/deeplo/internal/api"
	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/compose"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/ssh"
)

// Returns the function injected into the admin API server for
// POST /api/v1/refresh. It dials each configured host, runs docker compose ps,
// and returns the live service states grouped by host.
func buildRefreshFunc(env *bootstrap.Config, getConfig func() *config.Config, logger *slog.Logger) func(ctx context.Context) []api.RefreshHost {
	return func(ctx context.Context) []api.RefreshHost {
		deployConfig := getConfig()

		// Group projects by host to open one SSH connection per host.
		type hostWork struct {
			host     config.Host
			projects []config.Project
		}
		hostMap := make(map[string]*hostWork)
		var hostOrder []string
		for _, proj := range deployConfig.Projects {
			for _, tgtName := range proj.Targets {
				if _, exists := hostMap[tgtName]; !exists {
					h := findHost(deployConfig.Hosts, tgtName)
					if h == nil {
						continue
					}
					hostMap[tgtName] = &hostWork{host: *h}
					hostOrder = append(hostOrder, tgtName)
				}
				hostMap[tgtName].projects = append(hostMap[tgtName].projects, proj)
			}
		}

		dialer := ssh.NewDialer()
		hosts := make([]api.RefreshHost, len(hostOrder))

		var wg sync.WaitGroup
		for i, hostName := range hostOrder {
			wg.Add(1)
			go func(idx int, hostName string) {
				defer wg.Done()
				hw := hostMap[hostName]
				h := hw.host

				dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()

				conn, err := dialer.Dial(dialCtx, ssh.DialConfig{
					Address:        h.Address,
					Port:           h.EffectivePort(env.SSHPort),
					User:           h.EffectiveUser(env.SSHUser),
					PrivateKeyFile: env.SSHPrivateKeyFile,
					KnownHostsFile: env.SSHKnownHosts,
					HostKeyPolicy:  env.SSHHostKeyPolicy,
				})
				if err != nil {
					hosts[idx] = api.RefreshHost{Host: h.Name, Error: fmt.Sprintf("SSH dial: %v", err)}
					return
				}
				defer func() { _ = conn.Close() }()

				var projects []api.RefreshProject
				for _, proj := range hw.projects {
					remoteDir := path.Join(h.DeployDir, proj.DeploySubdir)
					executor := compose.NewExecutor(conn, remoteDir, proj.Name, logger)
					services, psErr := executor.Ps(dialCtx, proj.ComposeFiles)
					if psErr != nil {
						projects = append(projects, api.RefreshProject{Project: proj.Name, Error: psErr.Error()})
						continue
					}
					var svcList []api.RefreshService
					for _, svc := range services {
						svcList = append(svcList, api.RefreshService{Service: svc.Service, State: svc.State, Status: svc.Status})
					}
					projects = append(projects, api.RefreshProject{Project: proj.Name, Services: svcList})
				}
				hosts[idx] = api.RefreshHost{Host: h.Name, Projects: projects}
			}(i, hostName)
		}
		wg.Wait()
		return hosts
	}
}

func findHost(hosts []config.Host, name string) *config.Host {
	for i := range hosts {
		if hosts[i].Name == name {
			return &hosts[i]
		}
	}
	return nil
}
