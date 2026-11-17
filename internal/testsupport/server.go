// Package testsupport provides an in-process SSH/SFTP server for tests.
package testsupport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Server is a minimal in-process SSH server for hermetic tests. Exec requests
// are forwarded to /bin/sh on the test host, and the sftp subsystem is served
// by pkg/sftp against the real filesystem. Tests must use t.TempDir paths.
type Server struct {
	Addr       string
	Host       ssh.Signer
	Client     ssh.Signer
	KnownHosts string

	privateKey ed25519.PrivateKey
	listener   net.Listener
	config     *ssh.ServerConfig
	wg         sync.WaitGroup
	closed     atomic.Bool
	// Connections counts accepted TCP connections.
	Connections atomic.Int32
}

// Start creates a server listening on 127.0.0.1. The server accepts only the
// client key returned in Server.Client.
func Start(t *testing.T) *Server {
	t.Helper()
	_, host := generateSigner(t)
	clientKey, clientSigner := generateSigner(t)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(clientSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown public key")
		},
	}
	cfg.AddHostKey(host)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &Server{
		Addr:       ln.Addr().String(),
		Host:       host,
		Client:     clientSigner,
		privateKey: clientKey,
		listener:   ln,
		config:     cfg,
	}
	s.KnownHosts = knownhosts.Line([]string{knownhosts.Normalize(s.Addr)}, host.PublicKey())
	t.Cleanup(s.Close)
	s.wg.Add(1)
	go s.accept()
	return s
}

func generateSigner(t *testing.T) (ed25519.PrivateKey, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return priv, signer
}

// HostKeyString returns a one-line authorized key for the server host key.
func (s *Server) HostKeyString() string {
	return string(ssh.MarshalAuthorizedKey(s.Host.PublicKey()))
}

// PrivateKeyPEM returns the client key in OpenSSH PEM format.
func (s *Server) PrivateKeyPEM(t *testing.T) string {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(s.privateKey, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.Connections.Add(1)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(channel, requests)
	}
}

func (s *Server) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()
	for req := range requests {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &payload)
			_ = req.Reply(true, nil)
			cmd := exec.Command("/bin/sh", "-c", payload.Command)
			cmd.Stdin = channel
			cmd.Stdout = channel
			cmd.Stderr = channel.Stderr()
			err := cmd.Run()
			status := uint32(0)
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					status = uint32(exitErr.ExitCode())
				} else {
					status = 1
				}
			}
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			return
		case "subsystem":
			var payload struct{ Name string }
			_ = ssh.Unmarshal(req.Payload, &payload)
			if payload.Name != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			server, err := sftp.NewServer(channel)
			if err == nil {
				_ = server.Serve()
				_ = server.Close()
			}
			return
		case "env":
			_ = req.Reply(true, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// Close stops accepting connections and closes the listener.
func (s *Server) Close() {
	if s.closed.Swap(true) {
		return
	}
	_ = s.listener.Close()
}

// RejectServer accepts TCP connections and immediately closes them. It is used
// to exercise transient-connect retries.
func RejectServer(t *testing.T) (addr string, connections *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var count atomic.Int32
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			count.Add(1)
			_ = conn.Close()
		}
	}()
	return ln.Addr().String(), &count
}
