package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/pkg/sftp"
)

// ErrNotExist is matched by NotExistError.
var ErrNotExist = errors.New("remote path does not exist")

// WithSFTP runs fn with an SFTP client. If ctx expires, the SSH connection is
// closed so a stuck SFTP read or write cannot block the provider forever.
func (c *Client) WithSFTP(ctx context.Context, fn func(*sftp.Client) error) error {
	if err := ctx.Err(); err != nil {
		return contextOperationError("open sftp", err)
	}
	sc, err := c.openSFTP(ctx)
	if err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		done <- fn(sc)
	}()

	select {
	case err := <-done:
		if ctxErr := ctx.Err(); ctxErr != nil {
			c.abortSFTP(sc)
			return contextOperationError("sftp", ctxErr)
		}
		return c.closeSFTP(sc, err)
	case <-ctx.Done():
		// Break the socket first: sc.Close can wait for a peer that never
		// completes the channel teardown.
		c.abortSFTP(sc)
		return contextOperationError("sftp", ctx.Err())
	}
}

// openSFTP constructs an SFTP client without blocking past ctx. NewClient sends
// the subsystem request and waits for the version packet, both of which a
// stalled server can withhold forever.
func (c *Client) openSFTP(ctx context.Context) (*sftp.Client, error) {
	type result struct {
		client *sftp.Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		sc, err := sftp.NewClient(c.ssh)
		ch <- result{client: sc, err: err}
	}()

	select {
	case res := <-ch:
		if ctxErr := ctx.Err(); ctxErr != nil {
			if res.client != nil {
				go func() { _ = res.client.Close() }()
			}
			c.abort()
			return nil, contextOperationError("open sftp", ctxErr)
		}
		if res.err != nil {
			return nil, fmt.Errorf("open sftp: %w", res.err)
		}
		return res.client, nil
	case <-ctx.Done():
		// The buffered channel keeps the NewClient goroutine from leaking on
		// send; the reaper closes a client that arrives after cancellation.
		go func() {
			if res := <-ch; res.client != nil {
				_ = res.client.Close()
			}
		}()
		c.abort()
		return nil, contextOperationError("open sftp", ctx.Err())
	}
}

// abortSFTP force-closes the raw socket, then closes the SFTP client and SSH
// transport asynchronously.
func (c *Client) abortSFTP(sc *sftp.Client) {
	c.forceCloseRaw()
	if sc != nil {
		go func() { _ = sc.Close() }()
	}
	go func() { _ = c.Close() }()
}

// closeSFTP closes the SFTP client with a bounded grace period, force-closing
// the raw socket if the peer never completes the close handshake.
func (c *Client) closeSFTP(sc *sftp.Client, runErr error) error {
	done := make(chan error, 1)
	go func() { done <- sc.Close() }()
	select {
	case closeErr := <-done:
		if runErr != nil {
			return runErr
		}
		return closeErr
	case <-time.After(closeGrace):
		c.forceCloseRaw()
		// done is buffered, so the close goroutine can deliver its result and
		// exit without a receiver.
		if runErr != nil {
			return runErr
		}
		return fmt.Errorf("close sftp: timed out after %s", closeGrace)
	}
}

// SFTPStatusCode extracts an SFTP status code when present.
func SFTPStatusCode(err error) (uint32, bool) {
	var se *sftp.StatusError
	if errors.As(err, &se) {
		return se.Code, true
	}
	return 0, false
}

// IsPermissionError classifies permission denial using SFTP status codes.
func IsPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	code, ok := SFTPStatusCode(err)
	return ok && code == uint32(sftp.ErrSSHFxPermissionDenied)
}

// IsNotExistError classifies a missing path.
func IsNotExistError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	code, ok := SFTPStatusCode(err)
	return ok && code == uint32(sftp.ErrSSHFxNoSuchFile)
}

// NotExistError is returned by helpers when a path is absent.
type NotExistError struct{ Path string }

func (e *NotExistError) Error() string { return fmt.Sprintf("%s: %s", e.Path, ErrNotExist) }

func (e *NotExistError) Unwrap() error { return ErrNotExist }

// Is reports whether target is ErrNotExist.
func (e *NotExistError) Is(target error) bool { return target == ErrNotExist }
