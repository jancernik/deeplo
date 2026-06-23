package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"

	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type sshDialer struct{}

var knownHostsMu sync.Mutex

func NewDialer() Dialer {
	return &sshDialer{}
}

func (dialer *sshDialer) Dial(ctx context.Context, config DialConfig) (Connection, error) {
	privateKeyData, err := os.ReadFile(config.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read private key %q: %w", config.PrivateKeyFile, err)
	}
	signer, err := cryptossh.ParsePrivateKey(privateKeyData)
	if err != nil {
		return nil, fmt.Errorf("parse private key %q: %w", config.PrivateKeyFile, err)
	}

	hostKeyCallback, err := buildHostKeyCallback(config.KnownHostsFile, config.HostKeyPolicy)
	if err != nil {
		return nil, err
	}

	clientConfig := &cryptossh.ClientConfig{
		User:            config.User,
		Auth:            []cryptossh.AuthMethod{cryptossh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
	}

	address := fmt.Sprintf("%s:%d", config.Address, config.Port)

	netConnection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("tcp dial %s: %w", address, err)
	}

	sshConn, channels, reqs, err := cryptossh.NewClientConn(netConnection, address, clientConfig)
	if err != nil {
		_ = netConnection.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", address, err)
	}

	return newConnection(cryptossh.NewClient(sshConn, channels, reqs)), nil
}

func buildHostKeyCallback(knownHostsFile string, policy string) (cryptossh.HostKeyCallback, error) {
	if knownHostsFile == "" {
		if policy == "strict" {
			return nil, fmt.Errorf("strict host key policy requires a known_hosts file")
		}
		return func(string, net.Addr, cryptossh.PublicKey) error { return nil }, nil
	}

	if err := ensureKnownHostsFile(knownHostsFile); err != nil {
		return nil, err
	}
	verify, err := loadKnownHosts(knownHostsFile)
	if err != nil {
		return nil, err
	}

	if policy == "strict" {
		return verify, nil
	}
	return acceptNewCallback(knownHostsFile, verify), nil
}

func loadKnownHosts(knownHostsFile string) (cryptossh.HostKeyCallback, error) {
	callback, err := knownhosts.New(knownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts %q: %w", knownHostsFile, err)
	}
	return callback, nil
}

func acceptNewCallback(knownHostsFile string, verify cryptossh.HostKeyCallback) cryptossh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key cryptossh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) {
			if len(keyErr.Want) > 0 {
				return fmt.Errorf("host key mismatch for %s, refusing connection (key changed since last connection): %w", hostname, err)
			}
			return appendKnownHost(knownHostsFile, hostname, key)
		}

		return err
	}
}

func appendKnownHost(knownHostsFile, hostname string, key cryptossh.PublicKey) error {
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()

	f, err := os.OpenFile(knownHostsFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("open known_hosts %q for writing: %w", knownHostsFile, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Warn("close known_hosts file", "err", err)
		}
	}()

	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	_, err = fmt.Fprintln(f, line)
	return err
}

func ensureKnownHostsFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create known_hosts parent dir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("create known_hosts file %q: %w", path, err)
	}
	return f.Close()
}
