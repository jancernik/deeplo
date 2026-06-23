package ssh_test

// testserver_test.go implements a minimal in-process SSH server backed by the
// real local filesystem. Tests use this instead of a real remote host, so they
// run without any external dependencies.
//
// The server supports two channel types:
//   - exec: runs the command via sh -c on the local host
//   - subsystem sftp: serves the local filesystem via github.com/pkg/sftp
//
// Process lifetime rules:
//   • A running exec process is killed when the SSH *connection* drops
//     (connCtx is cancelled) OR when the client sends a "signal" channel
//     request (e.g. session.Signal in the client), OR when the session
//     channel is closed by the client.
//   • We deliberately do NOT watch stdin EOF to cancel: cryptossh sends
//     SSH_MSG_CHANNEL_EOF for stdin immediately when session.Stdin is nil,
//     which would kill every command before it started.

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

// testSSHServer is a minimal in-process SSH server.
type testSSHServer struct {
	t        *testing.T
	listener net.Listener
	hostKey  cryptossh.Signer
	authKey  cryptossh.PublicKey // only this public key is accepted
	wg       sync.WaitGroup
}

// testKeys holds the file paths needed by the SSH client under test.
type testKeys struct {
	privateKeyFile string
	knownHostsFile string
}

// startTestSSHServer starts an in-process SSH server on 127.0.0.1 with a
// random port and returns the server and client key material. The server is
// automatically stopped when the test ends.
func startTestSSHServer(t *testing.T) (*testSSHServer, testKeys) {
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

	// known_hosts format for non-standard ports: [host]:port key-type key-data\n
	knownHostsPath := filepath.Join(dir, "known_hosts")
	kf, err := os.Create(knownHostsPath)
	if err != nil {
		t.Fatalf("create known_hosts: %v", err)
	}
	// cryptossh.MarshalAuthorizedKey already appends \n
	entry := fmt.Sprintf("[127.0.0.1]:%d %s", port, cryptossh.MarshalAuthorizedKey(hostSigner.PublicKey()))
	if _, err := kf.WriteString(entry); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	if err := kf.Close(); err != nil {
		t.Fatalf("close known_hosts: %v", err)
	}

	srv := &testSSHServer{
		t:        t,
		listener: ln,
		hostKey:  hostSigner,
		authKey:  clientSigner.PublicKey(),
	}

	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		srv.acceptLoop()
	}()

	t.Cleanup(func() {
		_ = ln.Close()
		done := make(chan struct{})
		go func() { srv.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("testSSHServer: cleanup timed out waiting for goroutines")
		}
	})

	return srv, testKeys{
		privateKeyFile: privKeyPath,
		knownHostsFile: knownHostsPath,
	}
}

func (s *testSSHServer) port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// wrongKnownHostsFile returns a known_hosts file containing a *different* host
// key for this server's address, used to test host key rejection.
func (s *testSSHServer) wrongKnownHostsFile(t *testing.T) string {
	t.Helper()
	wrongPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate wrong key: %v", err)
	}
	wrongSigner, err := cryptossh.NewSignerFromKey(wrongPriv)
	if err != nil {
		t.Fatalf("wrong signer: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create wrong known_hosts: %v", err)
	}
	if _, err := fmt.Fprintf(f, "[127.0.0.1]:%d %s", s.port(), cryptossh.MarshalAuthorizedKey(wrongSigner.PublicKey())); err != nil {
		t.Fatalf("write wrong known_hosts: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close wrong known_hosts: %v", err)
	}
	return path
}

// server internals

func (s *testSSHServer) acceptLoop() {
	config := &cryptossh.ServerConfig{
		PublicKeyCallback: func(_ cryptossh.ConnMetadata, key cryptossh.PublicKey) (*cryptossh.Permissions, error) {
			if bytes.Equal(key.Marshal(), s.authKey.Marshal()) {
				return &cryptossh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unauthorized key")
		},
	}
	config.AddHostKey(s.hostKey)

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn, config)
		}()
	}
}

func (s *testSSHServer) handleConn(conn net.Conn, config *cryptossh.ServerConfig) {
	srvConn, chans, reqs, err := cryptossh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer srvConn.Close() //nolint:errcheck

	// connCtx is cancelled when the underlying SSH connection drops.
	connCtx, connCancel := context.WithCancel(context.Background())
	go func() {
		_ = srvConn.Wait()
		connCancel()
	}()
	defer connCancel()

	go cryptossh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(cryptossh.UnknownChannelType, "unknown channel type") //nolint:errcheck
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleSession(ch, requests, connCtx)
		}()
	}
}

func (s *testSSHServer) handleSession(ch cryptossh.Channel, reqs <-chan *cryptossh.Request, connCtx context.Context) {
	defer ch.Close() //nolint:errcheck

	for req := range reqs {
		switch req.Type {
		case "exec":
			cmdStr, ok := parseSSHString(req.Payload)
			if !ok {
				if req.WantReply {
					req.Reply(false, nil) //nolint:errcheck
				}
				continue
			}
			if req.WantReply {
				req.Reply(true, nil) //nolint:errcheck
			}

			// execCtx is cancelled on: SSH signal request, channel close (reqs
			// closed), or parent connection drop. It is NOT cancelled by stdin EOF.
			execCtx, execCancel := context.WithCancel(connCtx)
			go func() {
				defer execCancel()
				for r := range reqs {
					if r.Type == "signal" {
						execCancel()
					}
					if r.WantReply {
						r.Reply(false, nil) //nolint:errcheck
					}
				}
				// reqs closes when the channel is closed → execCtx is cancelled.
			}()

			s.runExec(ch, cmdStr, execCtx)
			execCancel() // ensure cleanup even if the goroutine above is still alive
			return

		case "subsystem":
			name, ok := parseSSHString(req.Payload)
			if !ok || name != "sftp" {
				if req.WantReply {
					req.Reply(false, nil) //nolint:errcheck
				}
				continue
			}
			if req.WantReply {
				req.Reply(true, nil) //nolint:errcheck
			}
			srv, err := sftp.NewServer(ch)
			if err != nil {
				s.t.Logf("sftp.NewServer: %v", err)
				return
			}
			if err := srv.Serve(); err != nil && err != io.EOF {
				s.t.Logf("sftp serve: %v", err)
			}
			return

		default:
			if req.WantReply {
				req.Reply(false, nil) //nolint:errcheck
			}
		}
	}
}

// runExec runs cmdStr under sh -c and sends the exit-status back to the client.
// execCtx is cancelled when the client signals or closes the channel; in that
// case the process is killed and no exit-status is sent (the channel is already
// closing).
func (s *testSSHServer) runExec(ch cryptossh.Channel, cmdStr string, execCtx context.Context) {
	cmd := exec.CommandContext(execCtx, "sh", "-c", cmdStr)
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()

	err := cmd.Run()

	// If the context was cancelled the channel is being torn down; skip sending
	// exit-status to avoid a race on the already-closing channel.
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
	ch.SendRequest("exit-status", false, payload) //nolint:errcheck
}

// parseSSHString reads the SSH wire-format string from payload:
// 4-byte big-endian length followed by that many bytes.
func parseSSHString(payload []byte) (string, bool) {
	if len(payload) < 4 {
		return "", false
	}
	n := binary.BigEndian.Uint32(payload[:4])
	if int(n) > len(payload)-4 {
		return "", false
	}
	return string(payload[4 : 4+n]), true
}
