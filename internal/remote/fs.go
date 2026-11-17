// Package remote implements remote filesystem semantics over SFTP and shell
// commands, including smart sudo escalation.
package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
)

// Type identifies the kind of remote path managed by the provider.
type Type int

const (
	// File is a regular file.
	File Type = iota
	// Directory is a directory.
	Directory
	// Symlink is a symbolic link.
	Symlink
	// Other is any non-regular, non-directory, non-symlink path such as a
	// FIFO, socket, or device.
	Other
)

func (t Type) String() string {
	switch t {
	case File:
		return "regular file"
	case Directory:
		return "directory"
	case Symlink:
		return "symbolic link"
	default:
		return "special file"
	}
}

// Stat is the provider-facing view of a remote path.
type Stat struct {
	Type Type
	Mode os.FileMode
	UID  int
	GID  int
	Size int64
}

// FS executes remote filesystem operations. Each operation uses SFTP and
// retries once with `sudo -n` only when the failure is a permission denial.
type FS struct {
	Client    *client.Client
	Timeout   time.Duration
	Umask     uint32
	AllowSudo bool
	// testMkdirChmod injects a chmod failure after mkdir for tests.
	testMkdirChmod func(path string) error
}

func (fs *FS) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if fs.Timeout > 0 {
		return context.WithTimeout(ctx, fs.Timeout)
	}
	return ctx, func() {}
}

func normalizeSFTPError(p string, err error) error {
	if err == nil {
		return nil
	}
	if client.IsNotExistError(err) {
		return &client.NotExistError{Path: p}
	}
	if client.IsPermissionError(err) {
		return fmt.Errorf("%w: %s", os.ErrPermission, p)
	}
	return err
}

// Lstat inspects a path without following a final symlink.
func (fs *FS) Lstat(ctx context.Context, p string) (*Stat, error) {
	ctx, cancel := fs.operationContext(ctx)
	defer cancel()

	var out *Stat
	err := fs.Client.WithSFTP(ctx, func(sc *sftp.Client) error {
		info, err := sc.Lstat(p)
		if err != nil {
			return err
		}
		out = statFromInfo(info)
		return nil
	})
	if err == nil {
		return out, nil
	}
	err = normalizeSFTPError(p, err)
	if !fs.AllowSudo || !client.IsPermissionError(err) {
		return nil, err
	}
	return fs.sudoLstat(ctx, p)
}

func (fs *FS) sudoLstat(ctx context.Context, p string) (*Stat, error) {
	isLink, err := fs.test(ctx, p, "-L")
	if err != nil {
		return nil, err
	}
	if isLink {
		return &Stat{Type: Symlink, Mode: os.ModeSymlink | 0777}, nil
	}
	exists, err := fs.test(ctx, p, "-e")
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, &client.NotExistError{Path: p}
	}
	res, err := fs.run(ctx, true, "", nil, "stat", "-c", "%f|%a|%u|%g|%s", "--", p)
	if err != nil {
		return nil, err
	}
	return parseSudoStat(p, string(res.Stdout))
}

func parseSudoStat(p, out string) (*Stat, error) {
	fields := strings.Split(strings.TrimSpace(out), "|")
	if len(fields) != 5 {
		return nil, fmt.Errorf("unexpected stat output for %q", p)
	}
	rawMode, err := strconv.ParseUint(fields[0], 16, 32)
	if err != nil {
		return nil, fmt.Errorf("parse file type for %q: %w", p, err)
	}
	mode, err := parseOctal(fields[1])
	if err != nil {
		return nil, fmt.Errorf("parse mode for %q: %w", p, err)
	}
	uid, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, fmt.Errorf("parse uid for %q: %w", p, err)
	}
	gid, err := strconv.Atoi(fields[3])
	if err != nil {
		return nil, fmt.Errorf("parse gid for %q: %w", p, err)
	}
	size, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse size for %q: %w", p, err)
	}
	st := &Stat{Type: typeFromRawMode(rawMode), Mode: mode, UID: uid, GID: gid, Size: size}
	if st.Type == Directory {
		st.Mode |= os.ModeDir
	}
	if st.Type == Symlink {
		st.Mode |= os.ModeSymlink
	}
	return st, nil
}

const (
	sIFMT  = 0170000
	sIFREG = 0100000
	sIFDIR = 0040000
	sIFLNK = 0120000
)

func typeFromRawMode(raw uint64) Type {
	switch raw & sIFMT {
	case sIFREG:
		return File
	case sIFDIR:
		return Directory
	case sIFLNK:
		return Symlink
	default:
		return Other
	}
}

func statFromInfo(info os.FileInfo) *Stat {
	st := &Stat{Mode: info.Mode(), Size: info.Size()}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		st.Type = Symlink
	case info.IsDir():
		st.Type = Directory
	case info.Mode().IsRegular():
		st.Type = File
	default:
		st.Type = Other
	}
	if raw, ok := info.Sys().(*sftp.FileStat); ok {
		st.UID = int(raw.UID)
		st.GID = int(raw.GID)
	}
	return st
}

// Stat inspects a path following symlinks. It is used for parent directories,
// where symlinks are allowed.
func (fs *FS) Stat(ctx context.Context, p string) (*Stat, error) {
	ctx, cancel := fs.operationContext(ctx)
	defer cancel()

	var out *Stat
	err := fs.Client.WithSFTP(ctx, func(sc *sftp.Client) error {
		info, err := sc.Stat(p)
		if err != nil {
			return err
		}
		out = statFromInfo(info)
		return nil
	})
	if err == nil {
		return out, nil
	}
	err = normalizeSFTPError(p, err)
	if !fs.AllowSudo || !client.IsPermissionError(err) {
		return nil, err
	}

	exists, err := fs.test(ctx, p, "-e")
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, &client.NotExistError{Path: p}
	}
	res, err := fs.run(ctx, true, "", nil, "stat", "-L", "-c", "%f|%a|%u|%g|%s", "--", p)
	if err != nil {
		return nil, err
	}
	return parseSudoStat(p, string(res.Stdout))
}

// testFalseExit is the dedicated exit status used by the test wrapper to mean
// "condition is false". Any other non-zero status, including sudo's own
// failure status 1, is an error and must never be interpreted as a false
// condition.
const testFalseExit = 42

func (fs *FS) test(ctx context.Context, p, op string) (bool, error) {
	switch op {
	case "-e", "-d", "-L":
	default:
		return false, fmt.Errorf("unsupported test operator %q", op)
	}
	_, err := fs.run(ctx, true, "", nil, "sh", "-c", `test `+op+` "$1" || exit 42`, "sh", p)
	return mapTestOutcome(err)
}

func mapTestOutcome(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	var exitErr *client.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.Code {
		case 0:
			return true, nil
		case testFalseExit:
			return false, nil
		}
	}
	return false, err
}

// ReadFile reads a remote file. A negative limit means no explicit limit. A
// positive limit returns client.ErrOutputLimit when the file is larger.
func (fs *FS) ReadFile(ctx context.Context, p string, limit int64) ([]byte, error) {
	ctx, cancel := fs.operationContext(ctx)
	defer cancel()

	var out []byte
	err := fs.Client.WithSFTP(ctx, func(sc *sftp.Client) error {
		f, err := sc.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if limit < 0 {
			out, err = io.ReadAll(f)
			return err
		}
		out, err = readLimited(f, limit)
		return err
	})
	if err != nil {
		err = normalizeSFTPError(p, err)
		if !fs.AllowSudo || !client.IsPermissionError(err) {
			return nil, err
		}
		res, sudoErr := fs.runLimit(ctx, true, int(limitOrLarge(limit)), nil, "cat", "--", p)
		if sudoErr != nil {
			return nil, sudoErr
		}
		if res.StdoutTruncated {
			return nil, client.ErrOutputLimit
		}
		return res.Stdout, nil
	}
	if limit >= 0 && int64(len(out)) > limit {
		return nil, client.ErrOutputLimit
	}
	return out, nil
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(r, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, client.ErrOutputLimit
	}
	return data, nil
}

// HashFile computes the SHA-256 of the full remote file.
func (fs *FS) HashFile(ctx context.Context, p string) (string, error) {
	ctx, cancel := fs.operationContext(ctx)
	defer cancel()

	var digest string
	err := fs.Client.WithSFTP(ctx, func(sc *sftp.Client) error {
		f, err := sc.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		digest = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err == nil {
		return digest, nil
	}
	err = normalizeSFTPError(p, err)
	if !fs.AllowSudo || !client.IsPermissionError(err) {
		return "", err
	}
	res, err := fs.run(ctx, true, "", nil, "sha256sum", "--", p)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(res.Stdout))
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", fmt.Errorf("unexpected sha256sum output for %q", p)
	}
	return strings.ToLower(fields[0]), nil
}

// WriteFileAtomic writes content through a same-directory temporary file and
// renames it over the target. Ownership names must already be resolved to
// numeric ids by the caller so no name lookup can fail after the target
// content is written. A -1 uid or gid leaves that field unchanged.
//
// The temporary file is restricted to 0600 before any content is written, its
// final mode and ownership are applied before the rename, and a failure before
// the rename leaves the previous target content untouched.
func (fs *FS) WriteFileAtomic(ctx context.Context, p string, content []byte, mode os.FileMode, uid, gid int) error {
	ctx, cancel := fs.operationContext(ctx)
	defer cancel()

	err := fs.writeFileAtomicSFTP(ctx, p, content, mode, uid, gid)
	if err == nil {
		return nil
	}
	if !fs.AllowSudo || !client.IsPermissionError(normalizeSFTPError(p, err)) {
		return normalizeSFTPError(p, err)
	}
	return fs.writeFileAtomicSudo(ctx, p, content, mode, uid, gid)
}

func (fs *FS) writeFileAtomicSFTP(ctx context.Context, p string, content []byte, mode os.FileMode, uid, gid int) error {
	return fs.Client.WithSFTP(ctx, func(sc *sftp.Client) error {
		dir := path.Dir(p)
		base := path.Base(p)
		var (
			tmp  *sftp.File
			name string
		)
		for i := 0; i < 10; i++ {
			name = path.Join(dir, "."+base+".tmp."+randomSuffix())
			f, err := sc.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
			if err == nil {
				tmp = f
				break
			}
			if !errors.Is(err, os.ErrExist) {
				return err
			}
		}
		if tmp == nil {
			return errors.New("could not allocate a temporary remote file")
		}
		cleanup := true
		defer func() {
			if cleanup {
				_ = sc.Remove(name)
			}
		}()

		// Close the plaintext readability window before writing any content.
		if err := sc.Chmod(name, 0600); err != nil {
			_ = tmp.Close()
			return err
		}
		if _, err := tmp.Write(content); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := sc.Chmod(name, mode); err != nil {
			return err
		}
		if err := chownSFTP(sc, name, uid, gid); err != nil {
			return err
		}
		if err := sc.PosixRename(name, p); err != nil {
			// Some servers do not implement posix-rename.
			if status, ok := client.SFTPStatusCode(err); ok && status == uint32(sftp.ErrSSHFxOpUnsupported) {
				if err := sc.Rename(name, p); err != nil {
					return err
				}
			} else {
				return err
			}
		}
		cleanup = false
		return nil
	})
}

func (fs *FS) writeFileAtomicSudo(ctx context.Context, p string, content []byte, mode os.FileMode, uid, gid int) error {
	res, err := fs.run(ctx, true, "", nil, "mktemp", "--", path.Join(path.Dir(p), "."+path.Base(p)+".tmp.XXXXXX"))
	if err != nil {
		return err
	}
	tmp := strings.TrimSpace(string(res.Stdout))
	if tmp == "" {
		return errors.New("mktemp returned an empty path")
	}
	cleanup := true
	defer func() {
		if cleanup {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = fs.run(cleanupCtx, true, "", nil, "rm", "-f", "--", tmp)
		}
	}()

	// mktemp already creates 0600, but make the guarantee explicit before the
	// plaintext content is written.
	if _, err := fs.run(ctx, true, "", nil, "chmod", "0600", "--", tmp); err != nil {
		return err
	}
	if _, err := fs.run(ctx, true, "", content, "dd", "of="+tmp, "status=none"); err != nil {
		return err
	}
	if _, err := fs.run(ctx, true, "", nil, "chmod", fmt.Sprintf("%04o", mode.Perm()), "--", tmp); err != nil {
		return err
	}
	if uid >= 0 || gid >= 0 {
		owner := chownArg(uid, gid)
		if _, err := fs.run(ctx, true, "", nil, "chown", owner, "--", tmp); err != nil {
			return err
		}
	}
	if _, err := fs.run(ctx, true, "", nil, "mv", "-f", "--", tmp, p); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// ErrDirectoryMayExist reports that a directory creation failed after the
// directory itself was created and best-effort cleanup did not remove it.
var ErrDirectoryMayExist = errors.New("remote directory may exist after a failed setup")

// Mkdir creates a single directory and applies mode explicitly. If chmod
// fails after mkdir, the new directory is removed best-effort.
func (fs *FS) Mkdir(ctx context.Context, p string, mode os.FileMode) error {
	ctx, cancel := fs.operationContext(ctx)
	defer cancel()

	err := fs.Client.WithSFTP(ctx, func(sc *sftp.Client) error {
		if err := sc.Mkdir(p); err != nil {
			return err
		}
		if fs.testMkdirChmod != nil {
			if err := fs.testMkdirChmod(p); err != nil {
				return fs.cleanupFailedMkdir(sc, p, err)
			}
			return nil
		}
		if err := sc.Chmod(p, mode); err != nil {
			return fs.cleanupFailedMkdir(sc, p, err)
		}
		return nil
	})
	if err == nil {
		return nil
	}
	err = normalizeSFTPError(p, err)
	if !fs.AllowSudo || !client.IsPermissionError(err) {
		return err
	}
	if _, sudoErr := fs.run(ctx, true, "", nil, "install", "-d", "-m", fmt.Sprintf("%04o", mode.Perm()), "--", p); sudoErr != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = fs.run(cleanupCtx, true, "", nil, "rmdir", "--", p)
		return sudoErr
	}
	return nil
}

func (fs *FS) cleanupFailedMkdir(sc *sftp.Client, p string, cause error) error {
	if err := sc.RemoveDirectory(p); err != nil {
		return fmt.Errorf("%w: %s: cleanup failed: %v", ErrDirectoryMayExist, p, err)
	}
	return cause
}

// RemoveFile removes a file or symlink at exactly p.
func (fs *FS) RemoveFile(ctx context.Context, p string) error {
	ctx, cancel := fs.operationContext(ctx)
	defer cancel()

	err := fs.Client.WithSFTP(ctx, func(sc *sftp.Client) error {
		return sc.Remove(p)
	})
	if err == nil {
		return nil
	}
	err = normalizeSFTPError(p, err)
	if client.IsNotExistError(err) {
		return nil
	}
	if !fs.AllowSudo || !client.IsPermissionError(err) {
		return err
	}
	_, err = fs.run(ctx, true, "", nil, "rm", "-f", "--", p)
	return err
}

// RemoveDirectory removes an empty directory. It never recurses.
func (fs *FS) RemoveDirectory(ctx context.Context, p string) error {
	ctx, cancel := fs.operationContext(ctx)
	defer cancel()

	err := fs.Client.WithSFTP(ctx, func(sc *sftp.Client) error {
		return sc.RemoveDirectory(p)
	})
	if err == nil {
		return nil
	}
	err = normalizeSFTPError(p, err)
	if client.IsNotExistError(err) {
		return nil
	}
	if !fs.AllowSudo || !client.IsPermissionError(err) {
		return err
	}
	_, err = fs.run(ctx, true, "", nil, "rmdir", "--", p)
	return err
}

// Chmod applies a mode.
func (fs *FS) Chmod(ctx context.Context, p string, mode os.FileMode) error {
	ctx, cancel := fs.operationContext(ctx)
	defer cancel()

	err := fs.Client.WithSFTP(ctx, func(sc *sftp.Client) error {
		return sc.Chmod(p, mode)
	})
	if err == nil {
		return nil
	}
	err = normalizeSFTPError(p, err)
	if !fs.AllowSudo || !client.IsPermissionError(err) {
		return err
	}
	_, err = fs.run(ctx, true, "", nil, "chmod", fmt.Sprintf("%04o", mode.Perm()), "--", p)
	return err
}

// Chown applies ownership. A -1 uid or gid leaves that field unchanged.
func (fs *FS) Chown(ctx context.Context, p string, uid, gid int) error {
	ctx, cancel := fs.operationContext(ctx)
	defer cancel()
	if uid < 0 && gid < 0 {
		return nil
	}

	err := fs.Client.WithSFTP(ctx, func(sc *sftp.Client) error {
		return chownSFTP(sc, p, uid, gid)
	})
	if err == nil {
		return nil
	}
	err = normalizeSFTPError(p, err)
	if !fs.AllowSudo || !client.IsPermissionError(err) {
		return err
	}
	_, err = fs.run(ctx, true, "", nil, "chown", chownArg(uid, gid), "--", p)
	return err
}

// chownSFTP applies ownership. pkg/sftp always encodes both fields, so a
// partial change first reads the current value that must be preserved.
func chownSFTP(sc *sftp.Client, p string, uid, gid int) error {
	if uid < 0 && gid < 0 {
		return nil
	}
	if uid >= 0 && gid >= 0 {
		return sc.Chown(p, uid, gid)
	}
	info, err := sc.Stat(p)
	if err != nil {
		return err
	}
	raw, ok := info.Sys().(*sftp.FileStat)
	if !ok {
		return fmt.Errorf("cannot read ownership of %q", p)
	}
	if uid < 0 {
		uid = int(raw.UID)
	}
	if gid < 0 {
		gid = int(raw.GID)
	}
	return sc.Chown(p, uid, gid)
}

func chownArg(uid, gid int) string {
	switch {
	case uid >= 0 && gid >= 0:
		return fmt.Sprintf("%d:%d", uid, gid)
	case uid >= 0:
		return strconv.Itoa(uid)
	default:
		return ":" + strconv.Itoa(gid)
	}
}

// EnsureParents creates missing ancestors. Created ancestors are unmanaged and
// use the directory default derived from the configured umask.
func (fs *FS) EnsureParents(ctx context.Context, p string) error {
	if !path.IsAbs(p) {
		return fmt.Errorf("path %q is not absolute", p)
	}
	dir := path.Dir(p)
	if dir == "/" || dir == "." {
		return nil
	}
	var chain []string
	for cur := dir; cur != "/" && cur != "."; cur = path.Dir(cur) {
		chain = append(chain, cur)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		cur := chain[i]
		st, err := fs.Stat(ctx, cur)
		if err != nil {
			if !client.IsNotExistError(err) {
				return err
			}
			mode := os.FileMode(0777 &^ fs.Umask)
			if err := fs.Mkdir(ctx, cur, mode); err != nil {
				return fmt.Errorf("create parent directory %q: %w", cur, err)
			}
			continue
		}
		if st.Type != Directory {
			return fmt.Errorf("parent %q is not a directory", cur)
		}
	}
	return nil
}

// ---- command helpers ----

func (fs *FS) run(ctx context.Context, sudo bool, _ string, stdin []byte, args ...string) (*client.ExecResult, error) {
	return fs.runLimit(ctx, sudo, client.DefaultMaxOutput, stdin, args...)
}

func (fs *FS) runLimit(ctx context.Context, sudo bool, maxStdout int, stdin []byte, args ...string) (*client.ExecResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, fs.TimeoutOrDefault())
	defer cancel()
	res, err := fs.Client.Run(runCtx, client.ExecOptions{
		Args:      args,
		Sudo:      sudo,
		Stdin:     stdin,
		Timeout:   fs.TimeoutOrDefault(),
		MaxStdout: maxStdout,
	})
	if err != nil {
		if res != nil && len(res.Stderr) > 0 {
			return res, fmt.Errorf("%w: %s", err, sanitizeStderr(res.Stderr))
		}
		return res, err
	}
	return res, nil
}

func limitOrLarge(limit int64) int64 {
	if limit < 0 {
		return 64 * 1024 * 1024
	}
	return limit + 1
}

// TimeoutOrDefault returns the operation timeout.
func (fs *FS) TimeoutOrDefault() time.Duration {
	if fs.Timeout > 0 {
		return fs.Timeout
	}
	return 5 * time.Minute
}

func sanitizeStderr(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 512 {
		s = s[:512] + "..."
	}
	return s
}

// UIDForName resolves a user name to a numeric uid.
func (fs *FS) UIDForName(ctx context.Context, name string) (int, error) {
	if err := ValidatePrincipalName(name); err != nil {
		return 0, err
	}
	res, err := fs.run(ctx, false, "", nil, "id", "-u", name)
	if err != nil {
		return 0, fmt.Errorf("resolve user %q: %w", name, err)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		return 0, fmt.Errorf("parse uid for user %q: %w", name, err)
	}
	return uid, nil
}

// GIDForName resolves a group name to a numeric gid.
func (fs *FS) GIDForName(ctx context.Context, name string) (int, error) {
	if err := ValidatePrincipalName(name); err != nil {
		return 0, err
	}
	res, err := fs.run(ctx, false, "", nil, "getent", "group", name)
	if err == nil {
		fields := strings.Split(strings.TrimSpace(string(res.Stdout)), ":")
		if len(fields) >= 3 {
			if gid, parseErr := strconv.Atoi(fields[2]); parseErr == nil {
				return gid, nil
			}
		}
	}
	res, err = fs.run(ctx, false, "", nil, "id", "-g", name)
	if err != nil {
		return 0, fmt.Errorf("resolve group %q: %w", name, err)
	}
	gid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		return 0, fmt.Errorf("parse gid for group %q: %w", name, err)
	}
	return gid, nil
}

// NameForUID resolves a numeric uid to a name. It returns "" when the lookup
// fails, which leaves state untouched for that field.
func (fs *FS) NameForUID(ctx context.Context, uid int) string {
	res, err := fs.run(ctx, false, "", nil, "getent", "passwd", strconv.Itoa(uid))
	if err != nil {
		return ""
	}
	fields := strings.Split(strings.TrimSpace(string(res.Stdout)), ":")
	if len(fields) < 1 || fields[0] == "" {
		return ""
	}
	return fields[0]
}

// NameForGID resolves a numeric gid to a name.
func (fs *FS) NameForGID(ctx context.Context, gid int) string {
	res, err := fs.run(ctx, false, "", nil, "getent", "group", strconv.Itoa(gid))
	if err != nil {
		return ""
	}
	fields := strings.Split(strings.TrimSpace(string(res.Stdout)), ":")
	if len(fields) < 1 || fields[0] == "" {
		return ""
	}
	return fields[0]
}

// ValidatePrincipalName restricts owner/group names to safe POSIX account
// syntax before they are passed as an argv.
func ValidatePrincipalName(name string) error {
	if name == "" {
		return errors.New("name must not be empty")
	}
	if len(name) > 256 {
		return errors.New("name is too long")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.', r == '@':
		default:
			return fmt.Errorf("name %q contains invalid characters", name)
		}
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("name %q must not start with '-'", name)
	}
	return nil
}

func parseOctal(s string) (os.FileMode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, err
	}
	return os.FileMode(v), nil
}
