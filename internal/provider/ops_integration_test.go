package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

func newOpsModel(t *testing.T, server *testsupport.Server, commands ...attr.Value) OpsModel {
	t.Helper()
	return OpsModel{
		Conn:     types.StringNull(),
		Sudo:     types.BoolValue(false),
		Timeout:  types.StringValue("5s"),
		Files:    types.ListNull(types.ObjectType{AttrTypes: fileOperationAttrTypes()}),
		Commands: types.ListValueMust(types.ObjectType{AttrTypes: commandOperationAttrTypes()}, commands),
		Runs:     types.ListNull(types.ObjectType{AttrTypes: runOperationAttrTypes()}),
		defaults: testServerDefaults(t, server),
	}
}

func commandObject(key, cmd string) attr.Value {
	return commandObjectWithTrim(key, cmd, types.BoolNull())
}

func commandObjectWithTrim(key, cmd string, trim types.Bool) attr.Value {
	return types.ObjectValueMust(commandOperationAttrTypes(), map[string]attr.Value{
		"key": types.StringValue(key),
		"command": types.ListValueMust(types.StringType, []attr.Value{
			types.StringValue("sh"), types.StringValue("-c"), types.StringValue(cmd),
		}),
		"index": types.Int64Value(0),
		"trim":  trim,
		"sudo":  types.BoolValue(false), "timeout": types.StringNull(),
	})
}

func fileObjectWithTrim(key, path string, trim types.Bool) attr.Value {
	return types.ObjectValueMust(fileOperationAttrTypes(), map[string]attr.Value{
		"key": types.StringValue(key), "path": types.StringValue(path),
		"index": types.Int64Value(0),
		"trim":  trim,
		"sudo":  types.BoolValue(false), "timeout": types.StringNull(),
	})
}

func runObjectWithTrim(key, script string, trim types.Bool) attr.Value {
	return types.ObjectValueMust(runOperationAttrTypes(), map[string]attr.Value{
		"key":     types.StringValue(key),
		"script":  types.StringValue(script),
		"shell":   types.StringValue("sh"),
		"envs":    types.MapNull(types.StringType),
		"index":   types.Int64Value(0),
		"trim":    trim,
		"sudo":    types.BoolValue(false),
		"timeout": types.StringNull(),
	})
}

func TestOpsTrimDefaults(t *testing.T) {
	server := testsupport.Start(t)
	file := filepath.Join(t.TempDir(), "padded.txt")
	if err := os.WriteFile(file, []byte("  file value  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	model := newOpsModel(t, server)
	model.Commands = types.ListValueMust(types.ObjectType{AttrTypes: commandOperationAttrTypes()}, []attr.Value{
		commandObject("command", "printf '  command value  \\n'"),
	})
	model.Runs = types.ListValueMust(types.ObjectType{AttrTypes: runOperationAttrTypes()}, []attr.Value{
		runObjectWithTrim("run", "printf '  run value  \\n'", types.BoolNull()),
	})
	model.Files = types.ListValueMust(types.ObjectType{AttrTypes: fileOperationAttrTypes()}, []attr.Value{
		fileObjectWithTrim("file", file, types.BoolNull()),
	})
	values, diags := model.Execute(context.Background(), false)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	want := map[string]string{"file": "file value", "command": "command value", "run": "run value"}
	for key, expected := range want {
		if values[key] != expected {
			t.Fatalf("%s = %q, want %q", key, values[key], expected)
		}
	}
}

func TestOpsTrimFalsePreservesWhitespace(t *testing.T) {
	server := testsupport.Start(t)
	file := filepath.Join(t.TempDir(), "exact.txt")
	if err := os.WriteFile(file, []byte("  file value  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	model := newOpsModel(t, server, commandObjectWithTrim("command", "printf '  command value  \\n'", types.BoolValue(false)))
	model.Runs = types.ListValueMust(types.ObjectType{AttrTypes: runOperationAttrTypes()}, []attr.Value{
		runObjectWithTrim("run", "printf '  run value  \\n'", types.BoolValue(false)),
	})
	model.Files = types.ListValueMust(types.ObjectType{AttrTypes: fileOperationAttrTypes()}, []attr.Value{
		fileObjectWithTrim("file", file, types.BoolValue(false)),
	})
	values, diags := model.Execute(context.Background(), false)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	want := map[string]string{
		"file":    "  file value  \n",
		"command": "  command value  \n",
		"run":     "  run value  \n",
	}
	for key, expected := range want {
		if values[key] != expected {
			t.Fatalf("%s = %q, want %q", key, values[key], expected)
		}
	}
}

func TestOpsTrimWhitespaceOnlyBecomesEmpty(t *testing.T) {
	server := testsupport.Start(t)
	model := newOpsModel(t, server, commandObject("blank", "printf ' \\t\\n\\n'"))
	values, diags := model.Execute(context.Background(), false)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if values["blank"] != "" {
		t.Fatalf("blank = %q, want empty string", values["blank"])
	}
}

func TestOpsSensitiveTrimUsesSameDefault(t *testing.T) {
	server := testsupport.Start(t)
	model := newOpsModel(t, server,
		commandObjectWithTrim("trimmed", "printf '  value  \\n'", types.BoolNull()),
		commandObjectWithTrim("exact", "printf '  value  \\n'", types.BoolValue(false)),
	)
	values, diags := model.Execute(context.Background(), true)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if values["trimmed"] != "value" {
		t.Fatalf("trimmed = %q", values["trimmed"])
	}
	if values["exact"] != "  value  \n" {
		t.Fatalf("exact = %q", values["exact"])
	}
}

func TestOpsUnknownTrimRejected(t *testing.T) {
	server := testsupport.Start(t)
	model := newOpsModel(t, server, commandObjectWithTrim("command", "printf value", types.BoolUnknown()))
	if _, diags := model.Execute(context.Background(), false); !diags.HasError() {
		t.Fatal("expected unknown trim diagnostic")
	}
}

func TestOpsOutputLimit(t *testing.T) {
	server := testsupport.Start(t)
	t.Setenv(outputLimitEnv, "16")
	model := newOpsModel(t, server, commandObject("big", "printf '0123456789abcdefghij'"))
	values, diags := model.Execute(context.Background(), false)
	if !diags.HasError() {
		t.Fatalf("expected output limit error, got values=%v", values)
	}
	if !strings.Contains(diags[0].Detail(), "limit") {
		t.Fatalf("diagnostic = %q", diags[0].Detail())
	}
}

func TestOpsSensitiveStderrSuppressed(t *testing.T) {
	server := testsupport.Start(t)
	model := newOpsModel(t, server, commandObject("secret", "echo supersecret >&2; exit 7"))
	_, diags := model.Execute(context.Background(), true)
	if !diags.HasError() {
		t.Fatal("expected command failure")
	}
	if strings.Contains(diags[0].Detail(), "supersecret") {
		t.Fatalf("sensitive stderr leaked: %q", diags[0].Detail())
	}
	if !strings.Contains(diags[0].Detail(), "status 7") {
		t.Fatalf("exit status missing: %q", diags[0].Detail())
	}
}

func TestOpsNonSensitiveStderrIncluded(t *testing.T) {
	server := testsupport.Start(t)
	model := newOpsModel(t, server, commandObject("plain", "echo diagnostic >&2; exit 3"))
	_, diags := model.Execute(context.Background(), false)
	if !diags.HasError() {
		t.Fatal("expected command failure")
	}
	if !strings.Contains(diags[0].Detail(), "diagnostic") {
		t.Fatalf("bounded stderr missing: %q", diags[0].Detail())
	}
}

func TestOpsExecuteSingleResultMap(t *testing.T) {
	server := testsupport.Start(t)
	for _, sensitive := range []bool{false, true} {
		model := newOpsModel(t, server,
			commandObject("alpha", "printf alpha"),
			commandObject("beta", "printf beta"),
		)
		values, diags := model.Execute(context.Background(), sensitive)
		if diags.HasError() {
			t.Fatalf("sensitive=%t: %v", sensitive, diags)
		}
		if len(values) != 2 || values["alpha"] != "alpha" || values["beta"] != "beta" {
			t.Fatalf("sensitive=%t: values = %#v", sensitive, values)
		}
	}
}

func TestOpsFileRejectsSpecialFile(t *testing.T) {
	server := testsupport.Start(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(p, 0600); err != nil {
		t.Skipf("mkfifo not supported: %v", err)
	}
	model := newOpsModel(t, server)
	model.Commands = types.ListNull(types.ObjectType{AttrTypes: commandOperationAttrTypes()})
	model.Files = types.ListValueMust(types.ObjectType{AttrTypes: fileOperationAttrTypes()}, []attr.Value{
		types.ObjectValueMust(fileOperationAttrTypes(), map[string]attr.Value{
			"key": types.StringValue("fifo"), "path": types.StringValue(p),
			"index": types.Int64Value(0),
			"trim":  types.BoolNull(),
			"sudo":  types.BoolValue(false), "timeout": types.StringNull(),
		}),
	})
	_, diags := model.Execute(context.Background(), false)
	if !diags.HasError() {
		t.Fatal("expected special file rejection")
	}
	if !strings.Contains(diags[0].Detail(), "not a regular file") {
		t.Fatalf("diagnostic = %q", diags[0].Detail())
	}
}
