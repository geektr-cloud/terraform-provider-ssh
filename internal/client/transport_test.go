package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

func newTestKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return signer, string(pem.EncodeToMemory(block))
}

// stallMode selects where the test server stops responding after a successful
// TCP and SSH handshake.
type stallMode int

const (
	stallChannelOpen stallMode = iota // never answer session channel opens
	stallExec                         // accept the channel, never answer exec
	stallSubsystem                    // accept the channel, never answer subsystem
)

type stallServer struct {
	addr       string
	privateKey string
}

func startStallServer(t *testing.T, mode stallMode) *stallServer {
	t.Helper()
	hostSigner, _ := newTestKey(t)
	clientSigner, clientKey := newTestKey(t)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown public key")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	s := &stallServer{addr: ln.Addr().String(), privateKey: clientKey}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn, cfg, mode)
		}
	}()
	return s
}

func (s *stallServer) handle(conn net.Conn, cfg *ssh.ServerConfig, mode stallMode) {
	defer func() { _ = conn.Close() }()
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()
	go ssh.DiscardRequests(reqs)

	switch mode {
	case stallChannelOpen:
		// Read and discard channel opens without accepting or rejecting them.
		for range chans {
		}
	case stallExec, stallSubsystem:
		for newChannel := range chans {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions")
				continue
			}
			channel, requests, err := newChannel.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer func() { _ = channel.Close() }()
				// Consume but never reply to exec/subsystem requests.
				for range requests {
				}
			}()
		}
	}
}

func (s *stallServer) config(t *testing.T) Config {
	t.Helper()
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	return Config{
		Endpoint: Endpoint{
			Host:                  host,
			Port:                  port,
			User:                  "tester",
			PrivateKey:            s.privateKey,
			InsecureIgnoreHostKey: true,
		},
		Timeout:    5 * time.Second,
		RetryDelay: 20 * time.Millisecond,
	}
}

func TestRunEstablishmentTimeout(t *testing.T) {
	cases := []struct {
		name string
		mode stallMode
	}{
		{"channel open stalls", stallChannelOpen},
		{"exec reply stalls", stallExec},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := startStallServer(t, tc.mode)
			c, err := Dial(context.Background(), server.config(t))
			if err != nil {
				t.Fatalf("dial: %v", err)
			}

			start := time.Now()
			_, err = c.Run(context.Background(), ExecOptions{
				Args:    []string{"true"},
				Timeout: 100 * time.Millisecond,
			})
			elapsed := time.Since(start)
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("expected timeout, got %v", err)
			}
			if elapsed > time.Second {
				t.Fatalf("Run returned after %s, want under 1s", elapsed)
			}

			closeStart := time.Now()
			_ = c.Close()
			if closeElapsed := time.Since(closeStart); closeElapsed > time.Second {
				t.Fatalf("Close returned after %s, want under 1s", closeElapsed)
			}
		})
	}
}

func TestRunCanceledIsNotTimeout(t *testing.T) {
	server := startStallServer(t, stallExec)
	c, err := Dial(context.Background(), server.config(t))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err = c.Run(ctx, ExecOptions{Args: []string{"true"}})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Run returned after %s, want under 1s", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatalf("canceled run must not report ErrTimeout: %v", err)
	}
}

func TestWithSFTPEstablishmentTimeout(t *testing.T) {
	cases := []struct {
		name string
		mode stallMode
	}{
		{"channel open stalls", stallChannelOpen},
		{"subsystem reply stalls", stallSubsystem},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := startStallServer(t, tc.mode)
			c, err := Dial(context.Background(), server.config(t))
			if err != nil {
				t.Fatalf("dial: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			start := time.Now()
			err = c.WithSFTP(ctx, func(*sftp.Client) error {
				return errors.New("callback must not run")
			})
			elapsed := time.Since(start)
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("expected timeout, got %v", err)
			}
			if elapsed > time.Second {
				t.Fatalf("WithSFTP returned after %s, want under 1s", elapsed)
			}

			closeStart := time.Now()
			_ = c.Close()
			if closeElapsed := time.Since(closeStart); closeElapsed > time.Second {
				t.Fatalf("Close returned after %s, want under 1s", closeElapsed)
			}
		})
	}
}

func TestWithSFTPCanceledIsNotTimeout(t *testing.T) {
	server := startStallServer(t, stallSubsystem)
	c, err := Dial(context.Background(), server.config(t))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err = c.WithSFTP(ctx, func(*sftp.Client) error { return nil })
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("WithSFTP returned after %s, want under 1s", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatalf("canceled sftp must not report ErrTimeout: %v", err)
	}
}

// TestHandshakeSurvivesCancellation guards the handshake watchdog race: a
// cancellation that lands around a successful handshake must not tear down the
// connection that Dial is about to return.
func TestHandshakeSurvivesCancellation(t *testing.T) {
	server := testsupport.Start(t)
	conf := &ssh.ClientConfig{
		User:            "tester",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(server.Client)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		raw, err := net.Dial("tcp", server.Addr)
		if err != nil {
			cancel()
			t.Fatalf("dial tcp: %v", err)
		}
		cl, err := handshake(ctx, raw, server.Addr, conf)
		if err != nil {
			cancel()
			t.Fatalf("handshake: %v", err)
		}
		// Cancel immediately after success; the stopped AfterFunc must not
		// close the connection later.
		cancel()
		time.Sleep(2 * time.Millisecond)
		session, err := cl.NewSession()
		if err != nil {
			_ = cl.Close()
			t.Fatalf("iteration %d: connection was closed after successful handshake: %v", i, err)
		}
		_ = session.Close()
		_ = cl.Close()
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	server := testsupport.Start(t)
	cfg := testConfig(t, server, server.PrivateKeyPEM(t))
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	start := time.Now()
	_ = c.Close()
	_ = c.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("double Close took %s", elapsed)
	}
}

func TestRetryableClassifications(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"channel connection failed", fmt.Errorf("wrapped: %w", &ssh.OpenChannelError{Reason: ssh.ConnectionFailed}), true},
		{"channel resource shortage", &ssh.OpenChannelError{Reason: ssh.ResourceShortage}, true},
		{"channel prohibited", &ssh.OpenChannelError{Reason: ssh.Prohibited}, false},
		{"channel unknown type", &ssh.OpenChannelError{Reason: ssh.UnknownChannelType}, false},
		{"auth failure", &ssh.ServerAuthError{}, false},
		{"canceled", context.Canceled, false},
		{
			"permanent dns",
			&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "missing.invalid", IsNotFound: true}},
			false,
		},
		{
			"transient dns",
			&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "i/o timeout", IsTimeout: true}},
			true,
		},
		{
			"connection refused",
			&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			true,
		},
		{
			"temporary errno",
			&net.OpError{Op: "dial", Net: "tcp", Err: syscall.EAGAIN},
			true,
		},
		{
			"permission denied",
			&net.OpError{Op: "dial", Net: "tcp", Err: syscall.EACCES},
			false,
		},
		{
			"invalid argument",
			fmt.Errorf("resolve: %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EINVAL}),
			false,
		},
		{
			"malformed address",
			&net.OpError{Op: "dial", Net: "tcp", Err: &net.AddrError{Err: "missing port in address", Addr: "example.com"}},
			false,
		},
		{
			"bare addr error",
			&net.AddrError{Err: "too many colons in address", Addr: "::1:22"},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryable(tc.err); got != tc.want {
				t.Fatalf("retryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// startJumpRejectServer accepts SSH connections and rejects every forwarded
// channel with a transient ConnectionFailed reason.
func startJumpRejectServer(t *testing.T) (string, string, *atomic.Int32) {
	t.Helper()
	hostSigner, _ := newTestKey(t)
	clientSigner, clientKey := newTestKey(t)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown public key")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var connections atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer func() { _ = conn.Close() }()
				sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer func() { _ = sshConn.Close() }()
				go ssh.DiscardRequests(reqs)
				for newChannel := range chans {
					_ = newChannel.Reject(ssh.ConnectionFailed, "target unreachable")
				}
			}()
		}
	}()
	return ln.Addr().String(), clientKey, &connections
}

func TestJumpChannelOpenFailureRetries(t *testing.T) {
	jumpAddr, keyPEM, connections := startJumpRejectServer(t)
	jumpHost, jumpPort, err := net.SplitHostPort(jumpAddr)
	if err != nil {
		t.Fatalf("split jump addr: %v", err)
	}

	cfg := Config{
		Endpoint: Endpoint{
			Host:                  "10.0.0.1",
			Port:                  "22",
			User:                  "target",
			PrivateKey:            keyPEM,
			InsecureIgnoreHostKey: true,
		},
		Jump: &Endpoint{
			Host:                  jumpHost,
			Port:                  jumpPort,
			User:                  "jump",
			PrivateKey:            keyPEM,
			InsecureIgnoreHostKey: true,
		},
		Timeout:    300 * time.Millisecond,
		RetryDelay: 20 * time.Millisecond,
	}

	_, err = Dial(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected forwarded channel failure")
	}
	var de *DialError
	if !errors.As(err, &de) {
		t.Fatalf("expected DialError, got %T: %v", err, err)
	}
	if de.Attempts < 2 {
		t.Fatalf("attempts = %d, want at least 2", de.Attempts)
	}
	if got := connections.Load(); got < 2 {
		t.Fatalf("jump connections = %d, want at least 2", got)
	}
}
