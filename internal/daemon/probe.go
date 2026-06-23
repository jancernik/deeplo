package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/jancernik/deeplo/internal/api"
	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/config"
	"github.com/jancernik/deeplo/internal/ssh"
)

func buildProbeFunc(env *bootstrap.Config, getConfig func() *config.Config) func(ctx context.Context) []api.ProbeHost {
	return func(ctx context.Context) []api.ProbeHost {
		hosts := getConfig().Hosts
		results := make([]api.ProbeHost, len(hosts))
		dialer := ssh.NewDialer()

		var wg sync.WaitGroup
		for i, host := range hosts {
			wg.Add(1)
			go func(idx int, host config.Host) {
				defer wg.Done()
				result := api.ProbeHost{Host: host.Name, Address: host.Address}

				dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()

				conn, err := dialer.Dial(dialCtx, ssh.DialConfig{
					Address:        host.Address,
					Port:           host.EffectivePort(env.SSHPort),
					User:           host.EffectiveUser(env.SSHUser),
					PrivateKeyFile: env.SSHPrivateKeyFile,
					KnownHostsFile: env.SSHKnownHosts,
					HostKeyPolicy:  env.SSHHostKeyPolicy,
				})
				if err != nil {
					result.Error = err.Error()
					results[idx] = result
					return
				}

				if err := ssh.Probe(dialCtx, conn); err != nil {
					result.Error = err.Error()
				} else {
					result.OK = true
				}
				_ = conn.Close()
				results[idx] = result
			}(i, host)
		}
		wg.Wait()
		return results
	}
}
