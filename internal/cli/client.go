package cli

import (
	"github.com/jancernik/deeplo/internal/bootstrap"
	"github.com/jancernik/deeplo/internal/client"
)

var adminSocket = bootstrap.UnixSocketPath

// Returns an API client pointed at the admin socket.
func daemonClient() *client.Client {
	return client.New(adminSocket())
}
