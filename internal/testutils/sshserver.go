package testutils

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	cryptossh "golang.org/x/crypto/ssh"
)

// A minimal in-process SSH server backed by the local filesystem.
//
// The server supports two channel types:
//   - exec: runs the command via sh -c on the local host
//   - subsystem sftp: serves the local filesystem via github.com/pkg/sftp
//
// The exec subprocess is killed when the SSH signal request is received,
// when the session channel is closed, or when the connection drops.
type SSHServer struct {
	t        *testing.T
	listener net.Listener
	hostKey  cryptossh.Signer
	authKey  cryptossh.PublicKey
	wg       sync.WaitGroup
}

// File paths to the generated key material for use by an SSH client.
type SSHKeys struct {
	PrivateKeyFile string
	KnownHostsFile string
}

// Starts an in-process SSH server on 127.0.0.1 on a random port and returns the
// server handle and client key files. The server is stopped automatically.
func StartSSHServer(t *testing.T) (*SSHServer, SSHKeys) {
	t.Helper()

	hostPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := cryptossh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	clientPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientSigner, err := cryptossh.NewSignerFromKey(clientPriv)
	if err != nil {
		t.Fatalf("client signer: %v", err)
	}

	dir := t.TempDir()
	privKeyPath := filepath.Join(dir, "id_rsa")
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(clientPriv),
	})
	if err := os.WriteFile(privKeyPath, privPEM, 0600); err != nil {
		t.Fatalf("write private key: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	knownHostsPath := filepath.Join(dir, "known_hosts")
	kf, err := os.Create(knownHostsPath)
	if err != nil {
		t.Fatalf("create known_hosts: %v", err)
	}
	if _, err := fmt.Fprintf(kf, "[127.0.0.1]:%d %s", port, cryptossh.MarshalAuthorizedKey(hostSigner.PublicKey())); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	if err := kf.Close(); err != nil {
		t.Fatalf("close known_hosts: %v", err)
	}

	sshServer := &SSHServer{
		t:        t,
		listener: ln,
		hostKey:  hostSigner,
		authKey:  clientSigner.PublicKey(),
	}
	sshServer.wg.Go(sshServer.acceptLoop)

	t.Cleanup(func() {
		_ = ln.Close()
		done := make(chan struct{})
		go func() { sshServer.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("SSHServer: cleanup timed out waiting for goroutines")
		}
	})

	return sshServer, SSHKeys{PrivateKeyFile: privKeyPath, KnownHostsFile: knownHostsPath}
}

// Returns the TCP port the server is listening on.
func (sshServer *SSHServer) Port() int {
	return sshServer.listener.Addr().(*net.TCPAddr).Port
}

func (sshServer *SSHServer) acceptLoop() {
	config := &cryptossh.ServerConfig{
		PublicKeyCallback: func(_ cryptossh.ConnMetadata, key cryptossh.PublicKey) (*cryptossh.Permissions, error) {
			if bytes.Equal(key.Marshal(), sshServer.authKey.Marshal()) {
				return &cryptossh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unauthorized key")
		},
	}
	config.AddHostKey(sshServer.hostKey)

	for {
		conn, err := sshServer.listener.Accept()
		if err != nil {
			return
		}
		sshServer.wg.Go(func() {
			sshServer.handleConn(conn, config)
		})
	}
}

func (sshServer *SSHServer) handleConn(conn net.Conn, config *cryptossh.ServerConfig) {
	srvConn, channels, requests, err := cryptossh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer srvConn.Close() //nolint:errcheck

	connCtx, connCancel := context.WithCancel(context.Background())
	go func() { _ = srvConn.Wait(); connCancel() }()
	defer connCancel()

	go cryptossh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(cryptossh.UnknownChannelType, "unknown channel type") //nolint:errcheck
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		sshServer.wg.Go(func() {
			sshServer.handleSession(channel, channelRequests, connCtx)
		})
	}
}

func (sshServer *SSHServer) handleSession(channel cryptossh.Channel, requests <-chan *cryptossh.Request, connCtx context.Context) {
	defer channel.Close() //nolint:errcheck

	for request := range requests {
		switch request.Type {
		case "exec":
			command, ok := ParseSSHString(request.Payload)
			if !ok {
				if request.WantReply {
					request.Reply(false, nil) //nolint:errcheck
				}
				continue
			}
			if request.WantReply {
				request.Reply(true, nil) //nolint:errcheck
			}
			execCtx, execCancel := context.WithCancel(connCtx)
			go func() {
				defer execCancel()
				for sessionRequest := range requests {
					if sessionRequest.Type == "signal" {
						execCancel()
					}
					if sessionRequest.WantReply {
						sessionRequest.Reply(false, nil) //nolint:errcheck
					}
				}
			}()
			sshServer.runExec(channel, command, execCtx)
			execCancel()
			return

		case "subsystem":
			name, ok := ParseSSHString(request.Payload)
			if !ok || name != "sftp" {
				if request.WantReply {
					request.Reply(false, nil) //nolint:errcheck
				}
				continue
			}
			if request.WantReply {
				request.Reply(true, nil) //nolint:errcheck
			}
			sftpServer, err := sftp.NewServer(channel)
			if err != nil {
				sshServer.t.Logf("sftp.NewServer: %v", err)
				return
			}
			if err := sftpServer.Serve(); err != nil && err != io.EOF {
				sshServer.t.Logf("sftp serve: %v", err)
			}
			return

		default:
			if request.WantReply {
				request.Reply(false, nil) //nolint:errcheck
			}
		}
	}
}

func (sshServer *SSHServer) runExec(channel cryptossh.Channel, command string, execCtx context.Context) {
	cmd := exec.CommandContext(execCtx, "sh", "-c", command)
	cmd.Stdout = channel
	cmd.Stderr = channel.Stderr()

	err := cmd.Run()
	if execCtx.Err() != nil {
		return
	}

	exitCode := uint32(0)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if code := exitErr.ExitCode(); code >= 0 {
				exitCode = uint32(code)
			} else {
				exitCode = 1
			}
		} else {
			exitCode = 1
		}
	}
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, exitCode)
	channel.SendRequest("exit-status", false, payload) //nolint:errcheck
}

func ParseSSHString(payload []byte) (string, bool) {
	if len(payload) < 4 {
		return "", false
	}
	n := binary.BigEndian.Uint32(payload[:4])
	if int(n) > len(payload)-4 {
		return "", false
	}
	return string(payload[4 : 4+n]), true
}
