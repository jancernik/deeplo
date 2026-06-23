package ssh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pkg/sftp"
	cryptossh "golang.org/x/crypto/ssh"
)

type sshConnection struct {
	client *cryptossh.Client
}

func newConnection(client *cryptossh.Client) *sshConnection {
	return &sshConnection{client: client}
}

func (connection *sshConnection) Run(ctx context.Context, cmd string) (stdout, stderr string, err error) {
	session, err := connection.client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("new SSH session: %w", err)
	}
	defer session.Close() //nolint:errcheck

	var stdoutBuffer, stderrBuffer bytes.Buffer
	session.Stdout = &stdoutBuffer
	session.Stderr = &stderrBuffer

	if err := session.Start(cmd); err != nil {
		return "", "", fmt.Errorf("start %q: %w", cmd, err)
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	select {
	case runErr := <-done:
		return stdoutBuffer.String(), stderrBuffer.String(), runErr
	case <-ctx.Done():
		_ = session.Signal(cryptossh.SIGTERM)
		_ = session.Close()
		<-done
		return stdoutBuffer.String(), stderrBuffer.String(), ctx.Err()
	}
}

func (connection *sshConnection) Upload(ctx context.Context, localPath, remotePath string) error {
	sftpClient, err := sftp.NewClient(connection.client)
	if err != nil {
		return fmt.Errorf("open SFTP session: %w", err)
	}
	defer func() {
		if err := sftpClient.Close(); err != nil {
			slog.Warn("close SFTP session", "err", err)
		}
	}()

	if err := sftpClient.MkdirAll(filepath.Dir(remotePath)); err != nil {
		return fmt.Errorf("mkdir %q: %w", filepath.Dir(remotePath), err)
	}

	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local %q: %w", localPath, err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			slog.Warn("close source file", "err", err)
		}
	}()

	dst, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("create remote %q: %w", remotePath, err)
	}
	defer func() {
		if err := dst.Close(); err != nil {
			slog.Warn("close remote file", "err", err)
		}
	}()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy to %q: %w", remotePath, err)
	}
	return nil
}

func (connection *sshConnection) Close() error {
	return connection.client.Close()
}
