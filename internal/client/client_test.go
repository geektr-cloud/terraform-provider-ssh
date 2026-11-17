package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

func testConfig(t *testing.T, server *testsupport.Server, privateKey string) Config {
	t.Helper()
	host, port, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Endpoint: Endpoint{
			Host:                  host,
			Port:                  port,
			User:                  "tester",
			PrivateKey:            privateKey,
			InsecureIgnoreHostKey: true,
		},
		Timeout:    5 * time.Second,
		RetryDelay: 50 * time.Millisecond,
	}
}

func TestDialAndRun(t *testing.T) {
	server := testsupport.Start(t)
	cfg := testConfig(t, server, server.PrivateKeyPEM(t))

	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	res, err := c.Run(context.Background(), ExecOptions{Args: []string{"printf", "%s", "hello"}, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := string(res.Stdout); got != "hello" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestRunQuotesArguments(t *testing.T) {
	server := testsupport.Start(t)
	cfg := testConfig(t, server, server.PrivateKeyPEM(t))
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	payload := "a b; echo injected $(id) 'quoted'"
	res, err := c.Run(context.Background(), ExecOptions{
		Args:    []string{"printf", "%s", payload},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v (%s)", err, res.Stderr)
	}
	if string(res.Stdout) != payload {
		t.Fatalf("stdout = %q, want literal %q", res.Stdout, payload)
	}
}

func TestAuthFailureIsNotRetried(t *testing.T) {
	server := testsupport.Start(t)
	other := testsupport.Start(t)
	cfg := testConfig(t, server, other.PrivateKeyPEM(t))
	cfg.RetryDelay = 10 * time.Millisecond
	cfg.Timeout = 2 * time.Second

	_, err := Dial(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected authentication failure")
	}
	var dialErr *DialError
	if !errors.As(err, &dialErr) {
		t.Fatalf("expected DialError, got %T: %v", err, err)
	}
	if dialErr.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", dialErr.Attempts)
	}
	if got := server.Connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want 1 (authentication failures must not retry)", got)
	}
}

func TestFixedHostKeyMismatchIsNotRetried(t *testing.T) {
	server := testsupport.Start(t)
	other := testsupport.Start(t)
	cfg := testConfig(t, server, server.PrivateKeyPEM(t))
	cfg.InsecureIgnoreHostKey = false
	cfg.HostKey = other.HostKeyString()
	cfg.RetryDelay = 10 * time.Millisecond
	cfg.Timeout = 2 * time.Second

	_, err := Dial(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected fixed host key mismatch")
	}
	if !IsHostKeyError(err) {
		t.Fatalf("expected host key error, got %v", err)
	}
	var dialErr *DialError
	if !errors.As(err, &dialErr) {
		t.Fatalf("expected DialError, got %T: %v", err, err)
	}
	if dialErr.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", dialErr.Attempts)
	}
	if got := server.Connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want 1 (fixed host key failures must not retry)", got)
	}
}

func TestKnownHostsMismatchIsNotRetried(t *testing.T) {
	server := testsupport.Start(t)
	other := testsupport.Start(t)
	cfg := testConfig(t, server, server.PrivateKeyPEM(t))
	cfg.InsecureIgnoreHostKey = false
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(other.KnownHosts+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.KnownHosts = path
	cfg.RetryDelay = 10 * time.Millisecond
	cfg.Timeout = 2 * time.Second

	_, err := Dial(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected host key mismatch")
	}
	if !IsHostKeyError(unwrapAll(err)) {
		t.Fatalf("expected host key error, got %v", err)
	}
	if got := server.Connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want 1 (host key failures must not retry)", got)
	}
}

func TestKnownHostsAndFixedHostKey(t *testing.T) {
	server := testsupport.Start(t)
	key := server.PrivateKeyPEM(t)

	// known_hosts mode
	cfg := testConfig(t, server, key)
	cfg.InsecureIgnoreHostKey = false
	cfg.KnownHosts = filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(cfg.KnownHosts, []byte(server.KnownHosts+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("known_hosts dial: %v", err)
	}
	_ = c.Close()

	// fixed host_key mode
	cfg2 := testConfig(t, server, key)
	cfg2.InsecureIgnoreHostKey = false
	cfg2.HostKey = server.HostKeyString()
	c2, err := Dial(context.Background(), cfg2)
	if err != nil {
		t.Fatalf("host_key dial: %v", err)
	}
	_ = c2.Close()
}

func TestTransientConnectionRetries(t *testing.T) {
	addr, connections := testsupport.RejectServer(t)
	host, port, _ := net.SplitHostPort(addr)
	cfg := Config{
		Endpoint: Endpoint{
			Host:                  host,
			Port:                  port,
			User:                  "tester",
			PrivateKey:            mustTestKey(t),
			InsecureIgnoreHostKey: true,
		},
		Timeout:    300 * time.Millisecond,
		RetryDelay: 20 * time.Millisecond,
	}
	_, err := Dial(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if connections.Load() < 2 {
		t.Fatalf("connections = %d, want at least 2 retries", connections.Load())
	}
}

func TestCommandTimeout(t *testing.T) {
	server := testsupport.Start(t)
	cfg := testConfig(t, server, server.PrivateKeyPEM(t))
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	start := time.Now()
	_, err = c.Run(context.Background(), ExecOptions{
		Args:    []string{"sleep", "5"},
		Timeout: 100 * time.Millisecond,
	})
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestValidateEndpoint(t *testing.T) {
	base := Endpoint{Host: "example", Port: "22", User: "u", PrivateKey: "key"}
	tests := []struct {
		name string
		ep   Endpoint
		ok   bool
	}{
		{"valid", base, true},
		{"port", Endpoint{Host: "h", Port: "0", User: "u", PrivateKey: "k"}, false},
		{"host", Endpoint{Host: "", Port: "22", User: "u", PrivateKey: "k"}, false},
		{"mode conflict", Endpoint{Host: "h", Port: "22", User: "u", PrivateKey: "k", KnownHosts: "/tmp/kh", HostKey: "ssh-ed25519 AAAA"}, false},
		{"insecure conflict", Endpoint{Host: "h", Port: "22", User: "u", PrivateKey: "k", InsecureIgnoreHostKey: true, KnownHosts: "/tmp/kh"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEndpoint(tc.ep, false)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"echo", "a b", "it's", "x;rm -rf /"})
	want := "echo 'a b' 'it'\\''s' 'x;rm -rf /'"
	if got != want {
		t.Fatalf("shellJoin = %q, want %q", got, want)
	}
	if strings.Contains(got, "`") {
		t.Fatalf("unexpected backtick in %q", got)
	}
}

func unwrapAll(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

func mustTestKey(t *testing.T) string {
	t.Helper()
	server := testsupport.Start(t)
	return server.PrivateKeyPEM(t)
}
