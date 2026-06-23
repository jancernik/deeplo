// Package ssh implements SSH-based remote host connectivity.
package ssh

import "context"

type DialConfig struct {
	Address        string
	Port           int
	User           string
	PrivateKeyFile string
	KnownHostsFile string
	HostKeyPolicy  string
}

// Executor runs commands on a remote host.
type Executor interface {
	Run(ctx context.Context, cmd string) (stdout, stderr string, err error)
}

// FileTransfer copies files to a remote host.
type FileTransfer interface {
	Upload(ctx context.Context, localPath, remotePath string) error
}

type Connection interface {
	Executor
	FileTransfer
	Close() error
}

type Dialer interface {
	Dial(ctx context.Context, config DialConfig) (Connection, error)
}
