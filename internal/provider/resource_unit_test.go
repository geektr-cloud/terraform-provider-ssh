package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
	"github.com/geektr-cloud/terraform-provider-ssh/internal/remote"
)

func resourceModelForTest(t *testing.T, dirs []attr.Value, files []attr.Value) sshResourceModel {
	t.Helper()
	return sshResourceModel{
		Conn:               types.StringNull(),
		ConnectionIdentity: types.StringNull(),
		Umask:              types.StringValue("022"),
		Owner:              types.StringNull(),
		UID:                types.Int64Null(),
		Group:              types.StringNull(),
		GID:                types.Int64Null(),
		Directories:        types.ListValueMust(types.ObjectType{AttrTypes: directoryAttrTypes()}, dirs),
		Files:              types.ListValueMust(types.ObjectType{AttrTypes: fileAttrTypes()}, files),
	}
}

func directoryObject(path, mode string, owner types.String, uid types.Int64) attr.Value {
	return types.ObjectValueMust(directoryAttrTypes(), map[string]attr.Value{
		"path": types.StringValue(path), "mode": types.StringValue(mode),
		"owner": owner, "uid": uid,
		"group": types.StringNull(), "gid": types.Int64Null(),
		"auto_import": types.BoolValue(false),
	})
}

func fileObject(path, content, wo string) attr.Value {
	var contentVal attr.Value = types.StringNull()
	if content != "" {
		contentVal = types.StringValue(content)
	}
	var woVal attr.Value = types.StringNull()
	if wo != "" {
		woVal = types.StringValue(wo)
	}
	return types.ObjectValueMust(fileAttrTypes(), map[string]attr.Value{
		"path": types.StringValue(path), "mode": types.StringNull(),
		"owner": types.StringNull(), "uid": types.Int64Null(),
		"group": types.StringNull(), "gid": types.Int64Null(),
		"auto_import":    types.BoolValue(false),
		"content":        contentVal,
		"content_wo":     woVal,
		"content_sha256": types.StringNull(),
	})
}

func TestValidateResourceModel(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		dirs  []attr.Value
		files []attr.Value
	}{
		{
			name:  "relative path",
			dirs:  []attr.Value{directoryObject("relative/path", "0755", types.StringNull(), types.Int64Null())},
			files: nil,
		},
		{
			name:  "root path",
			dirs:  []attr.Value{directoryObject("/", "0755", types.StringNull(), types.Int64Null())},
			files: nil,
		},
		{
			name:  "invalid mode",
			dirs:  []attr.Value{directoryObject("/tmp/x", "888", types.StringNull(), types.Int64Null())},
			files: nil,
		},
		{
			name:  "owner and uid",
			dirs:  []attr.Value{directoryObject("/tmp/x", "0755", types.StringValue("root"), types.Int64Value(0))},
			files: nil,
		},
		{
			name: "duplicate path",
			dirs: []attr.Value{directoryObject("/tmp/x", "0755", types.StringNull(), types.Int64Null())},
			files: []attr.Value{
				fileObject("/tmp/x", "a", ""),
			},
		},
		{
			name:  "both content sources",
			files: []attr.Value{fileObject("/tmp/x", "a", "b")},
		},
		{
			name:  "no content source",
			files: []attr.Value{fileObject("/tmp/x", "", "")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := resourceModelForTest(t, tc.dirs, tc.files)
			if diags := validateResourceModel(ctx, &model, testStaticDefaults(), false); !diags.HasError() {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateResourceModelValid(t *testing.T) {
	ctx := context.Background()
	model := resourceModelForTest(t,
		[]attr.Value{directoryObject("/tmp/x", "0755", types.StringNull(), types.Int64Null())},
		[]attr.Value{fileObject("/tmp/x/a", "content", "")},
	)
	if diags := validateResourceModel(ctx, &model, testStaticDefaults(), false); diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

func TestBuildDesiredDefaults(t *testing.T) {
	ctx := context.Background()
	dirs := []attr.Value{directoryObject("/tmp/x", "", types.StringNull(), types.Int64Null())}
	files := []attr.Value{fileObject("/tmp/x/a", "hello", "")}
	// Empty mode string is invalid for a real config, but this exercises the
	// default path by replacing it with null.
	dirModel := resourceModelForTest(t, dirs, files)
	var dirList []directoryModel
	var fileList []fileModel
	_ = dirModel.Directories.ElementsAs(ctx, &dirList, false)
	_ = dirModel.Files.ElementsAs(ctx, &fileList, false)
	dirList[0].Mode = types.StringNull()
	fileList[0].Mode = types.StringNull()
	dirModel.Directories = listDirectories(dirList)
	dirModel.Files = listFiles(fileList)

	desired, diags := buildDesired(ctx, &dirModel, &dirModel, 027, client.DefaultMaxOutput)
	if diags.HasError() {
		t.Fatalf("build desired: %v", diags)
	}
	if len(desired) != 2 {
		t.Fatalf("entries = %d", len(desired))
	}
	byPath := map[string]desiredEntry{}
	for _, e := range desired {
		byPath[e.path] = e
	}
	if got := byPath["/tmp/x"].mode.Perm(); got != 0750 {
		t.Fatalf("directory mode = %04o", got)
	}
	if got := byPath["/tmp/x/a"].mode.Perm(); got != 0640 {
		t.Fatalf("file mode = %04o", got)
	}
	if string(byPath["/tmp/x/a"].content) != "hello" {
		t.Fatalf("content = %q", byPath["/tmp/x/a"].content)
	}
	if byPath["/tmp/x"].kind != remote.Directory || byPath["/tmp/x/a"].kind != remote.File {
		t.Fatal("unexpected entry types")
	}
}

func TestWriteOnlyPlanApplyHashMismatchRejected(t *testing.T) {
	cfgFile := fileModel{
		Path:       types.StringValue("/tmp/secret"),
		Mode:       types.StringNull(),
		Owner:      types.StringNull(),
		UID:        types.Int64Null(),
		Group:      types.StringNull(),
		GID:        types.Int64Null(),
		AutoImport: types.BoolValue(false),
		Content:    types.StringNull(),
		ContentWO:  types.StringValue("apply-secret"),
	}
	planned := fileModel{ContentSHA256: types.StringValue(sha256Sum([]byte("planned-secret")))}
	_, err := desiredFromFile(cfgFile, planned, &sshResourceModel{}, 022, client.DefaultMaxOutput)
	if err == nil {
		t.Fatal("expected unstable content_wo to be rejected")
	}
	if strings.Contains(err.Error(), "planned-secret") || strings.Contains(err.Error(), "apply-secret") {
		t.Fatalf("error leaked write-only content: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "changed between plan and apply") {
		t.Fatalf("unexpected error: %q", err.Error())
	}
}

func TestWriteOnlyPlanApplyHashMatchAllowed(t *testing.T) {
	secret := "stable-secret"
	cfgFile := fileModel{
		Path:       types.StringValue("/tmp/stable"),
		Mode:       types.StringNull(),
		Owner:      types.StringNull(),
		UID:        types.Int64Null(),
		Group:      types.StringNull(),
		GID:        types.Int64Null(),
		AutoImport: types.BoolValue(false),
		Content:    types.StringNull(),
		ContentWO:  types.StringValue(secret),
	}
	planned := fileModel{ContentSHA256: types.StringValue(sha256Sum([]byte(secret)))}
	entry, err := desiredFromFile(cfgFile, planned, &sshResourceModel{}, 022, client.DefaultMaxOutput)
	if err != nil {
		t.Fatalf("matching hash rejected: %v", err)
	}
	if string(entry.content) != secret {
		t.Fatalf("content = %q", entry.content)
	}
}

func TestBuildDesiredRejectsUnstableWriteOnly(t *testing.T) {
	ctx := context.Background()
	path := "/tmp/unstable"
	planFiles := types.ListValueMust(types.ObjectType{AttrTypes: fileAttrTypes()}, []attr.Value{
		types.ObjectValueMust(fileAttrTypes(), map[string]attr.Value{
			"path": types.StringValue(path), "mode": types.StringNull(),
			"owner": types.StringNull(), "uid": types.Int64Null(),
			"group": types.StringNull(), "gid": types.Int64Null(),
			"auto_import":    types.BoolValue(false),
			"content":        types.StringNull(),
			"content_wo":     types.StringNull(),
			"content_sha256": types.StringValue(sha256Sum([]byte("planned"))),
		}),
	})
	cfgFiles := types.ListValueMust(types.ObjectType{AttrTypes: fileAttrTypes()}, []attr.Value{
		fileObject(path, "", "apply"),
	})
	plan := sshResourceModel{
		Directories: types.ListNull(types.ObjectType{AttrTypes: directoryAttrTypes()}),
		Files:       planFiles,
	}
	cfg := sshResourceModel{
		Directories: types.ListNull(types.ObjectType{AttrTypes: directoryAttrTypes()}),
		Files:       cfgFiles,
	}
	if _, diags := buildDesired(ctx, &cfg, &plan, 022, client.DefaultMaxOutput); !diags.HasError() {
		t.Fatal("expected buildDesired to reject unstable write-only content")
	}

	// The same value on both sides is accepted.
	planFiles = types.ListValueMust(types.ObjectType{AttrTypes: fileAttrTypes()}, []attr.Value{
		types.ObjectValueMust(fileAttrTypes(), map[string]attr.Value{
			"path": types.StringValue(path), "mode": types.StringNull(),
			"owner": types.StringNull(), "uid": types.Int64Null(),
			"group": types.StringNull(), "gid": types.Int64Null(),
			"auto_import":    types.BoolValue(false),
			"content":        types.StringNull(),
			"content_wo":     types.StringNull(),
			"content_sha256": types.StringValue(sha256Sum([]byte("stable"))),
		}),
	})
	cfgFiles = types.ListValueMust(types.ObjectType{AttrTypes: fileAttrTypes()}, []attr.Value{
		fileObject(path, "", "stable"),
	})
	plan.Files = planFiles
	cfg.Files = cfgFiles
	entries, diags := buildDesired(ctx, &cfg, &plan, 022, client.DefaultMaxOutput)
	if diags.HasError() {
		t.Fatalf("matching buildDesired failed: %v", diags)
	}
	if len(entries) != 1 || string(entries[0].content) != "stable" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestResolveUmaskUsesConfigWhenPlanUnknown(t *testing.T) {
	if _, err := resolveUmask(types.StringUnknown(), types.StringValue("022")); err == nil {
		t.Fatal("explicit unknown umask must be rejected at apply")
	}
	umask, err := resolveUmask(types.StringNull(), types.StringValue("027"))
	if err != nil || umask != 027 {
		t.Fatalf("null config umask = %04o, %v", umask, err)
	}
	umask, err = resolveUmask(types.StringNull(), types.StringNull())
	if err != nil || umask != 022 {
		t.Fatalf("default umask = %04o, %v", umask, err)
	}
}

func TestBuildDesiredUsesConfigWhenPlanUnknown(t *testing.T) {
	ctx := context.Background()
	unknownDir := types.ObjectValueMust(directoryAttrTypes(), map[string]attr.Value{
		"path": types.StringUnknown(), "mode": types.StringUnknown(),
		"owner": types.StringUnknown(), "uid": types.Int64Unknown(),
		"group": types.StringUnknown(), "gid": types.Int64Unknown(),
		"auto_import": types.BoolUnknown(),
	})
	unknownFile := types.ObjectValueMust(fileAttrTypes(), map[string]attr.Value{
		"path": types.StringUnknown(), "mode": types.StringUnknown(),
		"owner": types.StringUnknown(), "uid": types.Int64Unknown(),
		"group": types.StringUnknown(), "gid": types.Int64Unknown(),
		"auto_import":    types.BoolUnknown(),
		"content":        types.StringUnknown(),
		"content_wo":     types.StringNull(),
		"content_sha256": types.StringUnknown(),
	})
	plan := sshResourceModel{
		Umask:       types.StringUnknown(),
		Directories: types.ListValueMust(types.ObjectType{AttrTypes: directoryAttrTypes()}, []attr.Value{unknownDir}),
		Files:       types.ListValueMust(types.ObjectType{AttrTypes: fileAttrTypes()}, []attr.Value{unknownFile}),
	}
	cfg := sshResourceModel{
		Umask: types.StringNull(),
		Directories: types.ListValueMust(types.ObjectType{AttrTypes: directoryAttrTypes()}, []attr.Value{
			types.ObjectValueMust(directoryAttrTypes(), map[string]attr.Value{
				"path": types.StringValue("/tmp/from-config"), "mode": types.StringNull(),
				"owner": types.StringNull(), "uid": types.Int64Null(),
				"group": types.StringNull(), "gid": types.Int64Null(),
				"auto_import": types.BoolValue(true),
			}),
		}),
		Files: types.ListValueMust(types.ObjectType{AttrTypes: fileAttrTypes()}, []attr.Value{
			types.ObjectValueMust(fileAttrTypes(), map[string]attr.Value{
				"path": types.StringValue("/tmp/from-config/file"), "mode": types.StringNull(),
				"owner": types.StringNull(), "uid": types.Int64Null(),
				"group": types.StringNull(), "gid": types.Int64Null(),
				"auto_import":    types.BoolValue(true),
				"content":        types.StringValue("from config"),
				"content_wo":     types.StringNull(),
				"content_sha256": types.StringNull(),
			}),
		}),
	}
	entries, diags := buildDesired(ctx, &cfg, &plan, 022, client.DefaultMaxOutput)
	if diags.HasError() {
		t.Fatalf("buildDesired: %v", diags)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	byPath := map[string]desiredEntry{}
	for _, e := range entries {
		byPath[e.path] = e
	}
	if e := byPath["/tmp/from-config"]; e.kind != remote.Directory || !e.autoImport || e.mode.Perm() != 0755 {
		t.Fatalf("directory entry = %#v", e)
	}
	if e := byPath["/tmp/from-config/file"]; e.kind != remote.File || string(e.content) != "from config" || !e.autoImport || e.mode.Perm() != 0644 {
		t.Fatalf("file entry = %#v", e)
	}
}

func TestBuildDesiredContentLimit(t *testing.T) {
	base := fileModel{
		Path:       types.StringValue("/tmp/limited"),
		Mode:       types.StringNull(),
		Owner:      types.StringNull(),
		UID:        types.Int64Null(),
		Group:      types.StringNull(),
		GID:        types.Int64Null(),
		AutoImport: types.BoolValue(false),
	}
	plain := base
	plain.Content = types.StringValue("12345")
	plain.ContentWO = types.StringNull()
	if _, err := desiredFromFile(plain, fileModel{}, &sshResourceModel{}, 022, 4); err == nil {
		t.Fatal("expected oversized plain content to be rejected")
	}

	writeOnly := base
	writeOnly.Content = types.StringNull()
	writeOnly.ContentWO = types.StringValue("12345")
	if _, err := desiredFromFile(writeOnly, fileModel{}, &sshResourceModel{}, 022, 4); err != nil {
		t.Fatalf("write-only content must not be limited: %v", err)
	}
}

func TestApplyValidationRejectsUnknownBlockFields(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		dir  attr.Value
		file attr.Value
	}{
		{
			name: "directory owner unknown",
			dir: types.ObjectValueMust(directoryAttrTypes(), map[string]attr.Value{
				"path": types.StringValue("/tmp/x"), "mode": types.StringNull(),
				"owner": types.StringUnknown(), "uid": types.Int64Null(),
				"group": types.StringNull(), "gid": types.Int64Null(),
				"auto_import": types.BoolValue(false),
			}),
		},
		{
			name: "directory mode unknown",
			dir: types.ObjectValueMust(directoryAttrTypes(), map[string]attr.Value{
				"path": types.StringValue("/tmp/x"), "mode": types.StringUnknown(),
				"owner": types.StringNull(), "uid": types.Int64Null(),
				"group": types.StringNull(), "gid": types.Int64Null(),
				"auto_import": types.BoolValue(false),
			}),
		},
		{
			name: "file auto_import unknown",
			file: types.ObjectValueMust(fileAttrTypes(), map[string]attr.Value{
				"path": types.StringValue("/tmp/x"), "mode": types.StringNull(),
				"owner": types.StringNull(), "uid": types.Int64Null(),
				"group": types.StringNull(), "gid": types.Int64Null(),
				"auto_import":    types.BoolUnknown(),
				"content":        types.StringValue("x"),
				"content_wo":     types.StringNull(),
				"content_sha256": types.StringNull(),
			}),
		},
		{
			name: "file content unknown at apply",
			file: types.ObjectValueMust(fileAttrTypes(), map[string]attr.Value{
				"path": types.StringValue("/tmp/x"), "mode": types.StringNull(),
				"owner": types.StringNull(), "uid": types.Int64Null(),
				"group": types.StringNull(), "gid": types.Int64Null(),
				"auto_import":    types.BoolValue(false),
				"content":        types.StringUnknown(),
				"content_wo":     types.StringNull(),
				"content_sha256": types.StringNull(),
			}),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var dirs, files []attr.Value
			if tc.dir != nil {
				dirs = []attr.Value{tc.dir}
			}
			if tc.file != nil {
				files = []attr.Value{tc.file}
			}
			model := resourceModelForTest(t, dirs, files)
			if diags := validateResourceModel(ctx, &model, testStaticDefaults(), true); !diags.HasError() {
				t.Fatal("expected apply validation error")
			}
		})
	}
}

func TestPlanValidationSkipsUnknownContent(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		content types.String
		wo      types.String
	}{
		{"unknown content", types.StringUnknown(), types.StringNull()},
		{"unknown write-only", types.StringNull(), types.StringUnknown()},
		{"both unknown", types.StringUnknown(), types.StringUnknown()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := types.ObjectValueMust(fileAttrTypes(), map[string]attr.Value{
				"path": types.StringValue("/tmp/x"), "mode": types.StringNull(),
				"owner": types.StringNull(), "uid": types.Int64Null(),
				"group": types.StringNull(), "gid": types.Int64Null(),
				"auto_import":    types.BoolValue(false),
				"content":        tc.content,
				"content_wo":     tc.wo,
				"content_sha256": types.StringNull(),
			})
			model := resourceModelForTest(t, nil, []attr.Value{file})
			if diags := validateResourceModel(ctx, &model, testStaticDefaults(), false); diags.HasError() {
				t.Fatalf("plan validation must skip unknown content: %v", diags)
			}
		})
	}
}

func TestOmittedBlockOwnershipInheritsAtApply(t *testing.T) {
	ctx := context.Background()
	model := resourceModelForTest(t, []attr.Value{
		types.ObjectValueMust(directoryAttrTypes(), map[string]attr.Value{
			"path": types.StringValue("/tmp/x"), "mode": types.StringNull(),
			"owner": types.StringNull(), "uid": types.Int64Null(),
			"group": types.StringNull(), "gid": types.Int64Null(),
			"auto_import": types.BoolValue(false),
		}),
	}, nil)
	model.Owner = types.StringValue("root")
	if diags := validateResourceModel(ctx, &model, testStaticDefaults(), true); diags.HasError() {
		t.Fatalf("omitted ownership must inherit: %v", diags)
	}
}

func TestConnectionIdentityChanged(t *testing.T) {
	base := Profile{
		Host:                  stringValue("127.0.0.1"),
		Port:                  stringValue("22"),
		User:                  stringValue("tester"),
		PrivateKey:            stringValue("dummy"),
		InsecureIgnoreHostKey: boolValue(true),
	}
	first, known, err := resolveIdentity(base, Profile{})
	if err != nil || !known {
		t.Fatalf("identity: %q %v %v", first, known, err)
	}
	again, known, err := resolveIdentity(base, Profile{})
	if err != nil || !known || again != first {
		t.Fatalf("identity must be deterministic: %q %q %v %v", first, again, known, err)
	}
	changed := base
	changed.Port = stringValue("2222")
	second, _, _ := resolveIdentity(changed, Profile{})
	if second == first {
		t.Fatal("port change must change identity")
	}
	keyOnly := base
	keyOnly.PrivateKey = stringValue("different")
	unchanged, _, _ := resolveIdentity(keyOnly, Profile{})
	if unchanged != first {
		t.Fatal("private key change must not change identity")
	}
	withJump := base
	withJump.Jump = jumpField{Set: true, Value: JumpProfile{
		Host: stringValue("jump"), User: stringValue("u"), PrivateKey: stringValue("k"),
	}}
	jumped, _, _ := resolveIdentity(withJump, Profile{})
	if jumped == first {
		t.Fatal("adding a jump host must change identity")
	}

	if _, known, err := resolveIdentity(Profile{}, Profile{}); err != nil || known {
		t.Fatal("an incomplete identity must not be reported as known")
	}
	if _, known, err := resolveConnIdentity(base, types.StringUnknown()); err != nil || known {
		t.Fatal("an unknown conn value must not produce an identity")
	}
}
