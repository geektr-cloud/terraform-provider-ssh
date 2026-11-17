// Package client implements the SSH transport used by the provider. It owns
// dialing, host key verification, retries, bounded command execution, and SFTP
// operations.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// closeGrace bounds how long a graceful SSH/SFTP teardown may take before the
// underlying socket is force-closed. The raw socket is always closed first, so
// this only guards against a peer that never completes the SSH close handshake.
const closeGrace = 2 * time.Second

// Client is a live SSH connection. It is never shared between Terraform
// operations and callers must Close it.
type Client struct {
	ssh     *ssh.Client
	jump    *ssh.Client
	raw     net.Conn
	jumpRaw net.Conn
	cfg     Config

	closeOnce sync.Once
	closeErr  error
}

// Dial establishes and authenticates the configured SSH connection, including
// the optional single jump host. The timeout budget covers both hops and all
// retry attempts.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	retryDelay := cfg.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 5 * time.Second
	}

	deadline := time.Now().Add(timeout)
	attempts := 0
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if lastErr == nil {
				lastErr = context.DeadlineExceeded
			}
			return nil, &DialError{
				Host:     cfg.Host,
				Attempts: attempts,
				Err:      fmt.Errorf("connection budget exhausted: %w", lastErr),
			}
		}

		attempts++
		attemptCtx, cancel := context.WithTimeout(ctx, remaining)
		cl, err := dialOnce(attemptCtx, cfg)
		cancel()
		if err == nil {
			return cl, nil
		}
		lastErr = err

		if !retryable(err) {
			return nil, annotateDialError(err, cfg.Host, attempts)
		}
		if time.Now().Add(retryDelay).After(deadline) {
			return nil, &DialError{
				Host:     cfg.Host,
				Attempts: attempts,
				Err:      fmt.Errorf("retry budget exhausted: %w", err),
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryDelay):
		}
	}
}

// annotateDialError records how many attempts were actually made.
func annotateDialError(err error, host string, attempts int) error {
	var de *DialError
	if errors.As(err, &de) {
		de.Attempts = attempts
		if de.Host == "" {
			de.Host = host
		}
		return de
	}
	return &DialError{Host: host, Attempts: attempts, Err: err}
}

// DialError marks a failed connection establishment.
type DialError struct {
	Host     string
	Attempts int
	Err      error
}

func (e *DialError) Error() string {
	return fmt.Sprintf("connect to %s: %v", e.Host, e.Err)
}

func (e *DialError) Unwrap() error { return e.Err }

func dialOnce(ctx context.Context, cfg Config) (*Client, error) {
	targetKey, err := buildSigner(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	targetHostKey, err := buildHostKeyCallback(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	targetConf := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(targetKey)},
		HostKeyCallback: targetHostKey,
	}

	if cfg.Jump == nil {
		raw, err := dialTCP(ctx, endpointAddress(cfg.Endpoint))
		if err != nil {
			return nil, &DialError{Host: cfg.Host, Err: err}
		}
		sshClient, err := handshake(ctx, raw, endpointAddress(cfg.Endpoint), targetConf)
		if err != nil {
			_ = raw.Close()
			return nil, &DialError{Host: cfg.Host, Err: err}
		}
		return &Client{ssh: sshClient, raw: raw, cfg: cfg}, nil
	}

	jumpKey, err := buildSigner(*cfg.Jump)
	if err != nil {
		return nil, fmt.Errorf("jump host: %w", err)
	}
	jumpHostKey, err := buildHostKeyCallback(*cfg.Jump)
	if err != nil {
		return nil, fmt.Errorf("jump host: %w", err)
	}
	jumpConf := &ssh.ClientConfig{
		User:            cfg.Jump.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(jumpKey)},
		HostKeyCallback: jumpHostKey,
	}

	jumpRaw, err := dialTCP(ctx, endpointAddress(*cfg.Jump))
	if err != nil {
		return nil, &DialError{Host: cfg.Jump.Host, Err: err}
	}
	jumpSSH, err := handshake(ctx, jumpRaw, endpointAddress(*cfg.Jump), jumpConf)
	if err != nil {
		closePartial([]net.Conn{jumpRaw})
		return nil, &DialError{Host: cfg.Jump.Host, Err: err}
	}
	targetAddress := endpointAddress(cfg.Endpoint)
	targetRaw, err := jumpSSH.DialContext(ctx, "tcp", targetAddress)
	if err != nil {
		closePartial([]net.Conn{jumpRaw}, jumpSSH)
		return nil, &DialError{Host: cfg.Host, Err: fmt.Errorf("dial through jump host: %w", err)}
	}
	sshClient, err := handshake(ctx, targetRaw, targetAddress, targetConf)
	if err != nil {
		closePartial([]net.Conn{targetRaw, jumpRaw}, jumpSSH)
		return nil, &DialError{Host: cfg.Host, Err: err}
	}
	return &Client{ssh: sshClient, raw: targetRaw, jump: jumpSSH, jumpRaw: jumpRaw, cfg: cfg}, nil
}

// closePartial force-closes raw sockets before asynchronously closing SSH
// clients. Closing an ssh.Client first can block on channel teardown, so the
// socket must be broken first.
func closePartial(raws []net.Conn, clients ...*ssh.Client) {
	for _, raw := range raws {
		if raw != nil {
			_ = raw.Close()
		}
	}
	for _, cl := range clients {
		if cl == nil {
			continue
		}
		go func(cl *ssh.Client) { _ = cl.Close() }(cl)
	}
}

func dialTCP(ctx context.Context, address string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	return conn, nil
}

// handshake performs the SSH handshake. Cancellation closes raw from a watchdog
// goroutine. context.AfterFunc's stop result is authoritative: if stop reports
// that the callback already started, the handshake result is discarded so a
// successful return can never be torn down by a stale watchdog.
func handshake(ctx context.Context, raw net.Conn, address string, conf *ssh.ClientConfig) (*ssh.Client, error) {
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	conn, chans, reqs, err := ssh.NewClientConn(raw, address, conf)
	if !stop() {
		_ = raw.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			return nil, err
		}
		return nil, errors.New("ssh handshake aborted by context")
	}
	if err != nil {
		_ = raw.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return ssh.NewClient(conn, chans, reqs), nil
}

// Close terminates the target connection and the jump connection, if any. The
// raw sockets are closed first so SSH teardown cannot block on a stalled peer.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		var errs []error
		if c.raw != nil {
			if err := c.raw.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
		if c.jumpRaw != nil {
			if err := c.jumpRaw.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
		if c.ssh != nil {
			errs = append(errs, closeWithTimeout(c.ssh))
		}
		if c.jump != nil {
			errs = append(errs, closeWithTimeout(c.jump))
		}
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}

// closeWithTimeout bounds a graceful close. The caller has already closed the
// underlying socket, so a stuck close is reported instead of blocking forever.
func closeWithTimeout(closer io.Closer) error {
	done := make(chan error, 1)
	go func() { done <- closer.Close() }()
	select {
	case err := <-done:
		return err
	case <-time.After(closeGrace):
		return fmt.Errorf("close timed out after %s", closeGrace)
	}
}

// forceCloseRaw breaks both sockets. It is the first step of every abort path.
func (c *Client) forceCloseRaw() {
	if c.raw != nil {
		_ = c.raw.Close()
	}
	if c.jumpRaw != nil {
		_ = c.jumpRaw.Close()
	}
}

// abort breaks the transport without waiting on SSH teardown. Raw sockets are
// closed synchronously; the SSH clients are closed in the background.
func (c *Client) abort() {
	c.forceCloseRaw()
	go func() { _ = c.Close() }()
}

// abortSession unblocks a stalled SSH session: the raw socket is closed first,
// then the session (whose Close may wait on the peer) is closed asynchronously.
func (c *Client) abortSession(session *ssh.Session) {
	c.forceCloseRaw()
	if session != nil {
		go func() { _ = session.Close() }()
	}
	go func() { _ = c.Close() }()
}

// SSHClient exposes the underlying client for SFTP construction.
func (c *Client) SSHClient() *ssh.Client { return c.ssh }

// Config returns the configuration used for this connection.
func (c *Client) Config() Config { return c.cfg }

// Timeout returns the configured operation budget.
func (c *Client) Timeout() time.Duration {
	if c.cfg.Timeout > 0 {
		return c.cfg.Timeout
	}
	return 5 * time.Minute
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		return false
	}
	if IsHostKeyError(err) {
		return false
	}
	var authErr *ssh.ServerAuthError
	if errors.As(err, &authErr) {
		return false
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return false
	}

	// Resolve DNS failures before the generic net.Error checks so a permanent
	// "no such host" is never retried.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return false
		}
		if dnsErr.IsTimeout {
			return true
		}
		return dnsErr.IsTemporary
	}

	// A jump host rejects forwarded channels with an OpenChannelError. Only
	// transient rejection reasons are worth another attempt; prohibited and
	// unknown-channel-type failures are permanent.
	var openErr *ssh.OpenChannelError
	if errors.As(err, &openErr) {
		switch openErr.Reason {
		case ssh.ConnectionFailed, ssh.ResourceShortage:
			return true
		default:
			return false
		}
	}

	// Only explicit timeouts and temporary failures are retryable. There is no
	// blanket *net.OpError fallback: permanent errors such as EACCES, EINVAL,
	// or a malformed address must fail immediately.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var tempErr interface{ Temporary() bool }
	if errors.As(err, &tempErr) && tempErr.Temporary() {
		return true
	}

	for _, target := range []error{
		io.EOF, io.ErrUnexpectedEOF,
		syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ECONNABORTED,
		syscall.ETIMEDOUT, syscall.EAGAIN, syscall.EMFILE, syscall.EPIPE,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// IsHostKeyError reports a host key verification failure, which must never be
// retried. It covers both knownhosts mismatches and fixed host key mismatches.
func IsHostKeyError(err error) bool {
	if errors.Is(err, errHostKeyMismatch) {
		return true
	}
	var keyErr *knownhosts.KeyError
	return errors.As(err, &keyErr)
}
