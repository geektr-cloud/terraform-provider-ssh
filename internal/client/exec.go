package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// DefaultMaxOutput is the per-item output cap used by ssh_data. The SSH
// transport itself only needs a small cap for its own command helpers.
const DefaultMaxOutput = 512 * 1024

// ExecOptions describes one remote command invocation.
type ExecOptions struct {
	Args    []string
	Envs    map[string]string
	Sudo    bool
	Stdin   []byte
	Timeout time.Duration
	// MaxStdout and MaxStderr bound captured bytes. A zero value means the
	// default internal cap. The command keeps running until completion; excess
	// output is discarded and reported as truncation.
	MaxStdout int
	MaxStderr int
}

// ExecResult holds bounded command output.
type ExecResult struct {
	Stdout          []byte
	Stderr          []byte
	ExitCode        int
	StdoutTruncated bool
	StderrTruncated bool
}

// ErrOutputLimit indicates captured output exceeded the configured cap.
var ErrOutputLimit = errors.New("remote output exceeded the configured limit")

// ErrTimeout indicates an operation exceeded its budget.
var ErrTimeout = errors.New("remote operation timed out")

// Run executes argv without exposing it to user shell interpretation. Each
// argument is quoted for the remote POSIX shell.
func (c *Client) Run(ctx context.Context, opts ExecOptions) (*ExecResult, error) {
	if len(opts.Args) == 0 {
		return nil, errors.New("command must not be empty")
	}
	if opts.Timeout < 0 {
		return nil, errors.New("timeout must not be negative")
	}
	maxStdout := opts.MaxStdout
	if maxStdout <= 0 {
		maxStdout = DefaultMaxOutput
	}
	maxStderr := opts.MaxStderr
	if maxStderr <= 0 {
		maxStderr = 16 * 1024
	}

	command := buildCommand(opts)

	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	session, err := c.openSession(runCtx)
	if err != nil {
		return nil, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close()
		}
	}()

	stdout := newLimitedBuffer(maxStdout)
	stderr := newLimitedBuffer(maxStderr)
	session.Stdout = stdout
	session.Stderr = stderr
	if opts.Stdin != nil {
		session.Stdin = bytes.NewReader(opts.Stdin)
	}

	if err := runCtx.Err(); err != nil {
		closed = true
		c.abortSession(session)
		return nil, contextOperationError("start command", err)
	}

	startCh := make(chan error, 1)
	go func() { startCh <- session.Start(command) }()
	select {
	case err := <-startCh:
		if err != nil {
			if ctxErr := runCtx.Err(); ctxErr != nil {
				closed = true
				c.abortSession(session)
				return nil, contextOperationError("start command", ctxErr)
			}
			return nil, mapExecError(err)
		}
	case <-runCtx.Done():
		closed = true
		c.abortSession(session)
		return nil, contextOperationError("start command", runCtx.Err())
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- session.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waitCh:
		if ctxErr := runCtx.Err(); ctxErr != nil {
			closed = true
			c.abortSession(session)
			return nil, contextOperationError("run command", ctxErr)
		}
	case <-runCtx.Done():
		// Force-close the raw socket first so the stuck read/session teardown
		// cannot block the caller.
		closed = true
		c.abortSession(session)
		return nil, contextOperationError("run command", runCtx.Err())
	}

	res := &ExecResult{
		Stdout:          stdout.Bytes(),
		Stderr:          stderr.Bytes(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
	}
	if exitErr, ok := waitErr.(*ssh.ExitError); ok {
		res.ExitCode = exitErr.ExitStatus()
		return res, &ExitError{Code: res.ExitCode, Result: res}
	}
	if waitErr != nil {
		return res, fmt.Errorf("wait for command: %w", waitErr)
	}
	return res, nil
}

// openSession creates an SSH session without blocking past ctx. If the peer
// never answers the channel-open request, the raw socket is closed and the
// late-arriving session (if any) is reaped asynchronously.
func (c *Client) openSession(ctx context.Context) (*ssh.Session, error) {
	type result struct {
		session *ssh.Session
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		session, err := c.ssh.NewSession()
		ch <- result{session: session, err: err}
	}()

	select {
	case res := <-ch:
		if ctxErr := ctx.Err(); ctxErr != nil {
			if res.session != nil {
				go func() { _ = res.session.Close() }()
			}
			c.abort()
			return nil, contextOperationError("open session", ctxErr)
		}
		if res.err != nil {
			return nil, fmt.Errorf("open session: %w", res.err)
		}
		return res.session, nil
	case <-ctx.Done():
		// The buffered channel lets NewSession finish without leaking a blocked
		// sender; the reaper closes any session that arrives late.
		go func() {
			if res := <-ch; res.session != nil {
				_ = res.session.Close()
			}
		}()
		c.abort()
		return nil, contextOperationError("open session", ctx.Err())
	}
}

// contextOperationError classifies cancellation. Only a deadline is reported as
// ErrTimeout; an explicit cancellation stays a cancellation.
func contextOperationError(op string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %s: %v", ErrTimeout, op, err)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s canceled: %w", op, err)
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}

// ExitError reports a non-zero remote exit status.
type ExitError struct {
	Code   int
	Result *ExecResult
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.Code)
}

func buildCommand(opts ExecOptions) string {
	args := append([]string(nil), opts.Args...)
	if len(opts.Envs) > 0 {
		keys := make([]string, 0, len(opts.Envs))
		for k := range opts.Envs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		envArgs := []string{"env"}
		for _, k := range keys {
			envArgs = append(envArgs, k+"="+opts.Envs[k])
		}
		args = append(envArgs, args...)
	}
	if opts.Sudo {
		args = append([]string{"sudo", "-n"}, args...)
	}
	if opts.Timeout > 0 {
		seconds := int64(opts.Timeout.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		args = append([]string{"timeout", "--signal=TERM", strconv.FormatInt(seconds, 10) + "s"}, args...)
	}
	return shellJoin(args)
}

// shellJoin quotes every argument using POSIX single-quote rules. The remote
// login shell only sees the joined argv, never shell operators supplied by the
// caller.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Safe characters do not need quoting. Everything else is wrapped in
	// single quotes with embedded single quotes rewritten as '\''.
	safe := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("-_./:=,@%+", r):
		default:
			safe = false
		}
		if !safe {
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func mapExecError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s", ErrTimeout, err)
	}
	return err
}

// limitedBuffer captures at most max bytes and records truncation.
type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func newLimitedBuffer(maxBytes int) *limitedBuffer {
	return &limitedBuffer{max: maxBytes}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, b.buf.Len())
	copy(out, b.buf.Bytes())
	return out
}

func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

var _ io.Writer = (*limitedBuffer)(nil)
