package provider

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
	"github.com/geektr-cloud/terraform-provider-ssh/internal/remote"
	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

func newRemoteFSForTest(t *testing.T) (*remote.FS, string) {
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
	return &remote.FS{Client: c, Timeout: 5 * time.Second, Umask: 022}, t.TempDir()
}

func TestUpsertResolvesOwnershipBeforeWriting(t *testing.T) {
	fs, dir := newRemoteFSForTest(t)
	ctx := context.Background()
	p := filepath.Join(dir, "file")

	entry := desiredEntry{
		path:      p,
		kind:      remote.File,
		mode:      0600,
		content:   []byte("new"),
		ownership: ownershipSpec{owner: types.StringValue("no-such-user-for-test"), group: types.StringNull(), uid: types.Int64Null(), gid: types.Int64Null()},
	}

	// New target: no file may appear when the owner name is invalid.
	if err := upsertEntry(ctx, fs, entry, false); err == nil {
		t.Fatal("expected invalid owner error")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("target created despite invalid owner: %v", err)
	}

	// Existing target: its old content must survive.
	if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := upsertEntry(ctx, fs, entry, true); err == nil {
		t.Fatal("expected invalid owner error on existing target")
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != "old" {
		t.Fatalf("target content = %q, %v", data, err)
	}
}

func TestUpsertCleansNewDirectoryAfterOwnershipFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chown to another uid would succeed")
	}
	fs, dir := newRemoteFSForTest(t)
	ctx := context.Background()
	p := filepath.Join(dir, "created")

	entry := desiredEntry{
		path:      p,
		kind:      remote.Directory,
		mode:      0755,
		ownership: ownershipSpec{owner: types.StringNull(), uid: types.Int64Value(1234567), group: types.StringNull(), gid: types.Int64Null()},
	}
	if err := upsertEntry(ctx, fs, entry, false); err == nil {
		t.Fatal("expected ownership failure")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("new directory was not cleaned up: %v", err)
	}
}

func TestRemoveManagedPathRequiresExpectedType(t *testing.T) {
	fs, dir := newRemoteFSForTest(t)
	ctx := context.Background()
	p := filepath.Join(dir, "file")
	if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := removeManagedPath(ctx, fs, p, remote.Directory); err == nil {
		t.Fatal("expected type mismatch error")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file was removed despite type mismatch: %v", err)
	}

	sub := filepath.Join(dir, "dir")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedPath(ctx, fs, sub, remote.File); err == nil {
		t.Fatal("expected directory type mismatch error")
	}
	if _, err := os.Stat(sub); err != nil {
		t.Fatalf("directory was removed despite type mismatch: %v", err)
	}

	if err := removeManagedPath(ctx, fs, p, remote.File); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := removeManagedPath(ctx, fs, sub, remote.Directory); err != nil {
		t.Fatalf("remove directory: %v", err)
	}
}

func TestBuildDesiredInheritsTopLevelOwnershipFromConfig(t *testing.T) {
	ctx := context.Background()
	dirs := func(owner types.String, uid types.Int64) types.List {
		return types.ListValueMust(types.ObjectType{AttrTypes: directoryAttrTypes()}, []attr.Value{
			types.ObjectValueMust(directoryAttrTypes(), map[string]attr.Value{
				"path":        types.StringValue("/tmp/inherit"),
				"mode":        types.StringNull(),
				"owner":       owner,
				"uid":         uid,
				"group":       types.StringNull(),
				"gid":         types.Int64Null(),
				"auto_import": types.BoolValue(false),
			}),
		})
	}
	emptyFiles := types.ListNull(types.ObjectType{AttrTypes: fileAttrTypes()})

	build := func(cfgOwner types.String, cfgUID types.Int64, blockOwner types.String, blockUID types.Int64) desiredEntry {
		t.Helper()
		plan := sshResourceModel{
			Umask:       types.StringValue("022"),
			Owner:       types.StringNull(),
			UID:         types.Int64Null(),
			Group:       types.StringNull(),
			GID:         types.Int64Null(),
			Directories: dirs(types.StringUnknown(), types.Int64Unknown()),
			Files:       emptyFiles,
		}
		cfg := sshResourceModel{
			Umask:       types.StringValue("022"),
			Owner:       cfgOwner,
			UID:         cfgUID,
			Group:       types.StringNull(),
			GID:         types.Int64Null(),
			Directories: dirs(blockOwner, blockUID),
			Files:       emptyFiles,
		}
		entries, diags := buildDesired(ctx, &cfg, &plan, 022, client.DefaultMaxOutput)
		if diags.HasError() {
			t.Fatalf("buildDesired: %v", diags)
		}
		if len(entries) != 1 {
			t.Fatalf("entries = %d", len(entries))
		}
		return entries[0]
	}

	entry := build(types.StringValue("root"), types.Int64Null(), types.StringNull(), types.Int64Null())
	if entry.ownership.owner.ValueString() != "root" || !entry.ownership.uid.IsNull() {
		t.Fatalf("inherited ownership = %#v", entry.ownership)
	}

	entry = build(types.StringValue("root"), types.Int64Null(), types.StringValue("alice"), types.Int64Null())
	if entry.ownership.owner.ValueString() != "alice" || !entry.ownership.uid.IsNull() {
		t.Fatalf("explicit owner = %#v", entry.ownership)
	}

	entry = build(types.StringValue("root"), types.Int64Null(), types.StringNull(), types.Int64Value(1000))
	if !entry.ownership.owner.IsNull() || entry.ownership.uid.ValueInt64() != 1000 {
		t.Fatalf("explicit uid = %#v", entry.ownership)
	}
}

func TestFillMetadataClearsUnknownOwnership(t *testing.T) {
	fs, _ := newRemoteFSForTest(t)
	ctx := context.Background()
	mode := types.StringUnknown()
	owner := types.StringUnknown()
	uid := types.Int64Unknown()
	group := types.StringUnknown()
	gid := types.Int64Unknown()
	st := &remote.Stat{Type: remote.File, Mode: 0644, UID: 999999, GID: 999999}
	fillMetadata(ctx, fs, &mode, &owner, &uid, &group, &gid, st)
	if mode.IsUnknown() || owner.IsUnknown() || uid.IsUnknown() || group.IsUnknown() || gid.IsUnknown() {
		t.Fatalf("metadata still unknown: mode=%v owner=%v uid=%v group=%v gid=%v", mode, owner, uid, group, gid)
	}
	if mode.ValueString() != "0644" {
		t.Fatalf("mode = %q", mode.ValueString())
	}
}

func TestReadManagedPathsKeepsEntriesOnInspectionError(t *testing.T) {
	fs, _ := newRemoteFSForTest(t)
	_ = fs.Client.Close()
	ctx := context.Background()
	state := resourceModelForTest(t,
		[]attr.Value{directoryObject("/tmp/keep", "0755", types.StringNull(), types.Int64Null())},
		[]attr.Value{fileObject("/tmp/keep/file", "content", "")},
	)
	diags := readManagedPaths(ctx, fs, &state)
	if !diags.HasError() {
		t.Fatal("expected inspection error")
	}
	dirs, _ := decodeDirectories(ctx, state.Directories)
	files, _ := decodeFiles(ctx, state.Files)
	if len(dirs) != 1 || dirs[0].Path.ValueString() != "/tmp/keep" {
		t.Fatalf("directory state was dropped: %#v", dirs)
	}
	if len(files) != 1 || files[0].Path.ValueString() != "/tmp/keep/file" {
		t.Fatalf("file state was dropped: %#v", files)
	}
}

func TestFillMetadataPreservesExplicitOwnership(t *testing.T) {
	fs, _ := newRemoteFSForTest(t)
	ctx := context.Background()
	mode := types.StringValue("0644")
	owner := types.StringValue("0")
	uid := types.Int64Value(0)
	group := types.StringValue("legacygroup")
	gid := types.Int64Value(0)
	st := &remote.Stat{Type: remote.File, Mode: 0644, UID: 0, GID: 0}
	fillMetadata(ctx, fs, &mode, &owner, &uid, &group, &gid, st)
	if owner.ValueString() != "0" {
		t.Fatalf("explicit owner changed to %q", owner.ValueString())
	}
	if group.ValueString() != "legacygroup" {
		t.Fatalf("explicit group changed to %q", group.ValueString())
	}
	if uid.ValueInt64() != 0 || gid.ValueInt64() != 0 {
		t.Fatalf("numeric ids = %d:%d", uid.ValueInt64(), gid.ValueInt64())
	}
}

func TestUpsertRejectsSpecialFile(t *testing.T) {
	fs, dir := newRemoteFSForTest(t)
	ctx := context.Background()
	p := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(p, 0600); err != nil {
		t.Skipf("mkfifo not supported: %v", err)
	}
	entry := desiredEntry{path: p, kind: remote.File, mode: 0600, content: []byte("x"), ownership: ownershipSpec{}}
	err := upsertEntry(ctx, fs, entry, true)
	if err == nil || !strings.Contains(err.Error(), "special file") {
		t.Fatalf("expected special file rejection, got %v", err)
	}
	if _, statErr := os.Stat(p); statErr != nil {
		t.Fatalf("special file was removed: %v", statErr)
	}
}
