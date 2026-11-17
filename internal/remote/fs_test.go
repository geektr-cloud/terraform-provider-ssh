package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

func newTestFS(t *testing.T) (*FS, string) {
	t.Helper()
	server := testsupport.Start(t)
	host, port, _ := net.SplitHostPort(server.Addr)
	c, err := client.Dial(context.Background(), client.Config{
		Endpoint: client.Endpoint{
			Host:                  host,
			Port:                  port,
			User:                  "tester",
			PrivateKey:            server.PrivateKeyPEM(t),
			InsecureIgnoreHostKey: true,
		},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &FS{Client: c, Timeout: 5 * time.Second, Umask: 022}, t.TempDir()
}

func TestFileCRUD(t *testing.T) {
	fs, dir := newTestFS(t)
	ctx := context.Background()
	p := filepath.Join(dir, "nested", "file.txt")

	if err := fs.EnsureParents(ctx, p); err != nil {
		t.Fatalf("ensure parents: %v", err)
	}
	if err := fs.WriteFileAtomic(ctx, p, []byte("hello world"), 0640, -1, -1); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, err := fs.Lstat(ctx, p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Type != File {
		t.Fatalf("type = %v, want file", st.Type)
	}
	if st.Mode.Perm() != 0640 {
		t.Fatalf("mode = %04o, want 0640", st.Mode.Perm())
	}
	data, err := fs.ReadFile(ctx, p, -1)
	if err != nil || string(data) != "hello world" {
		t.Fatalf("read = %q, %v", data, err)
	}
	digest, err := fs.HashFile(ctx, p)
	if err != nil || len(digest) != 64 {
		t.Fatalf("hash = %q, %v", digest, err)
	}

	// Atomic overwrite must not leave temp files behind.
	if err := fs.WriteFileAtomic(ctx, p, []byte("second"), 0600, -1, -1); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	data, _ = fs.ReadFile(ctx, p, -1)
	if string(data) != "second" {
		t.Fatalf("read after rewrite = %q", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if e.Name() != "file.txt" {
			t.Fatalf("unexpected temp entry %q", e.Name())
		}
	}

	if err := fs.RemoveFile(ctx, p); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := fs.Lstat(ctx, p); !client.IsNotExistError(err) {
		t.Fatalf("expected missing path, got %v", err)
	}
}

func TestReadFileLimit(t *testing.T) {
	fs, dir := newTestFS(t)
	ctx := context.Background()
	p := filepath.Join(dir, "big")
	if err := fs.WriteFileAtomic(ctx, p, []byte("0123456789"), 0600, -1, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(ctx, p, 4); !errors.Is(err, client.ErrOutputLimit) {
		t.Fatalf("expected output limit, got %v", err)
	}
	data, err := fs.ReadFile(ctx, p, 10)
	if err != nil || string(data) != "0123456789" {
		t.Fatalf("exact limit read = %q, %v", data, err)
	}
}

func TestSymlinkParentAllowed(t *testing.T) {
	fs, dir := newTestFS(t)
	ctx := context.Background()
	targetDir := filepath.Join(dir, "real")
	if err := fs.Mkdir(ctx, targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(targetDir, linkDir); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	p := filepath.Join(linkDir, "nested", "file")
	if err := fs.EnsureParents(ctx, p); err != nil {
		t.Fatalf("ensure parents through symlink: %v", err)
	}
	if err := fs.WriteFileAtomic(ctx, p, []byte("through link"), 0644, -1, -1); err != nil {
		t.Fatalf("write through symlink parent: %v", err)
	}
	realPath := filepath.Join(targetDir, "nested", "file")
	data, err := fs.ReadFile(ctx, realPath, -1)
	if err != nil || string(data) != "through link" {
		t.Fatalf("read real path = %q, %v", data, err)
	}
}

func TestDirectoryNotEmpty(t *testing.T) {
	fs, dir := newTestFS(t)
	ctx := context.Background()
	sub := filepath.Join(dir, "sub")
	if err := fs.EnsureParents(ctx, sub); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mkdir(ctx, sub, 0750); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(sub, "child")
	if err := fs.WriteFileAtomic(ctx, child, []byte("x"), 0600, -1, -1); err != nil {
		t.Fatal(err)
	}
	if err := fs.RemoveDirectory(ctx, sub); err == nil {
		t.Fatal("expected non-empty directory removal to fail")
	}
	if err := fs.RemoveFile(ctx, child); err != nil {
		t.Fatal(err)
	}
	if err := fs.RemoveDirectory(ctx, sub); err != nil {
		t.Fatalf("remove empty directory: %v", err)
	}
}

func TestSymlinkDetection(t *testing.T) {
	fs, dir := newTestFS(t)
	ctx := context.Background()
	target := filepath.Join(dir, "target")
	if err := fs.WriteFileAtomic(ctx, target, []byte("x"), 0600, -1, -1); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	st, err := fs.Lstat(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink mode, got %v", st.Mode)
	}
	if st.Type != Symlink {
		t.Fatalf("type = %v, want symlink", st.Type)
	}
}

func TestLstatClassifiesFileTypes(t *testing.T) {
	fs, dir := newTestFS(t)
	ctx := context.Background()

	regular := filepath.Join(dir, "regular")
	if err := fs.WriteFileAtomic(ctx, regular, []byte("x"), 0600, -1, -1); err != nil {
		t.Fatal(err)
	}
	if st, err := fs.Lstat(ctx, regular); err != nil || st.Type != File {
		t.Fatalf("regular type = %v, %v", st, err)
	}

	sub := filepath.Join(dir, "sub")
	if err := fs.Mkdir(ctx, sub, 0755); err != nil {
		t.Fatal(err)
	}
	if st, err := fs.Lstat(ctx, sub); err != nil || st.Type != Directory {
		t.Fatalf("directory type = %v, %v", st, err)
	}

	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Skipf("mkfifo not supported: %v", err)
	}
	if st, err := fs.Lstat(ctx, fifo); err != nil || st.Type != Other {
		t.Fatalf("fifo type = %v, %v", st, err)
	}
	if st, err := fs.Stat(ctx, fifo); err != nil || st.Type != Other {
		t.Fatalf("fifo stat type = %v, %v", st, err)
	}
}

func TestMkdirChmodFailureCleansUp(t *testing.T) {
	fs, dir := newTestFS(t)
	ctx := context.Background()
	fs.testMkdirChmod = func(string) error { return errors.New("injected chmod failure") }
	p := filepath.Join(dir, "newdir")
	if err := fs.Mkdir(ctx, p, 0755); err == nil {
		t.Fatal("expected mkdir failure")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("directory was not cleaned up: %v", err)
	}
}

func TestMapTestOutcome(t *testing.T) {
	if ok, err := mapTestOutcome(nil); !ok || err != nil {
		t.Fatalf("nil outcome = %v, %v", ok, err)
	}
	if ok, err := mapTestOutcome(&client.ExitError{Code: 0}); !ok || err != nil {
		t.Fatalf("exit 0 outcome = %v, %v", ok, err)
	}
	if ok, err := mapTestOutcome(&client.ExitError{Code: testFalseExit}); ok || err != nil {
		t.Fatalf("false outcome = %v, %v", ok, err)
	}
	// sudo's own exit status 1 must remain an error, never "condition false".
	sudoErr := &client.ExitError{Code: 1}
	if ok, err := mapTestOutcome(sudoErr); ok || !errors.Is(err, sudoErr) {
		t.Fatalf("sudo failure outcome = %v, %v", ok, err)
	}
	wrapped := fmt.Errorf("wrapped: %w", &client.ExitError{Code: 1})
	if _, err := mapTestOutcome(wrapped); err == nil {
		t.Fatal("wrapped non-condition failure must be an error")
	}
}

func TestWriteFileAtomicAppliesOwnership(t *testing.T) {
	fs, dir := newTestFS(t)
	ctx := context.Background()
	p := filepath.Join(dir, "owned")
	uid, gid := os.Getuid(), os.Getgid()
	if err := fs.WriteFileAtomic(ctx, p, []byte("owned"), 0640, uid, gid); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, err := fs.Lstat(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if st.UID != uid || st.GID != gid {
		t.Fatalf("ownership = %d:%d, want %d:%d", st.UID, st.GID, uid, gid)
	}
	if st.Mode.Perm() != 0640 {
		t.Fatalf("mode = %04o", st.Mode.Perm())
	}
}

func TestWriteFileAtomicOwnershipFailureLeavesTargetUntouched(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chown to another uid would succeed")
	}
	fs, dir := newTestFS(t)
	ctx := context.Background()
	p := filepath.Join(dir, "target")

	// New target: the rename must not happen when ownership fails.
	if err := fs.WriteFileAtomic(ctx, p, []byte("new"), 0600, 1234567, -1); err == nil {
		t.Fatal("expected ownership failure")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("target exists after failed ownership: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("temporary files left behind: %v", entries)
	}

	// Existing target: old content must survive a failed ownership change.
	if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFileAtomic(ctx, p, []byte("new"), 0600, 1234567, -1); err == nil {
		t.Fatal("expected ownership failure")
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != "old" {
		t.Fatalf("target content = %q, %v", data, err)
	}
	entries, _ = os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "target" {
			t.Fatalf("temporary file left behind: %q", e.Name())
		}
	}
}

func TestParseHelpers(t *testing.T) {
	if got, err := parseOctal("0750"); err != nil || got.Perm() != 0750 {
		t.Fatalf("parseOctal = %v, %v", got, err)
	}
	if err := ValidatePrincipalName("bad name"); err == nil {
		t.Fatal("expected invalid principal name error")
	}
	if err := ValidatePrincipalName("root"); err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[uint64]Type{
		0100644: File,
		0040755: Directory,
		0120777: Symlink,
		0010644: Other,
	} {
		if got := typeFromRawMode(raw); got != want {
			t.Fatalf("typeFromRawMode(%o) = %v, want %v", raw, got, want)
		}
	}
}
