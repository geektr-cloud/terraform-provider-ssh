package provider

import (
	"context"
	"net"
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
	host, port, _ := net.SplitHostPort(server.Addr)
	return OpsModel{
		Conn: &ConnectionModel{
			Type:                  types.StringValue("linux"),
			Host:                  types.StringValue(host),
			Port:                  types.StringValue(port),
			User:                  types.StringValue("tester"),
			PrivateKey:            types.StringValue(server.PrivateKeyPEM(t)),
			KnownHosts:            types.StringNull(),
			HostKey:               types.StringNull(),
			InsecureIgnoreHostKey: types.BoolValue(true),
			Envs:                  types.MapValueMust(types.StringType, map[string]attr.Value{}),
			Timeout:               types.StringValue("5s"),
			RetryDelay:            types.StringValue("50ms"),
		},
		Sudo:     types.BoolValue(false),
		Timeout:  types.StringValue("5s"),
		Files:    types.ListNull(types.ObjectType{AttrTypes: fileOperationAttrTypes()}),
		Commands: types.ListValueMust(types.ObjectType{AttrTypes: commandOperationAttrTypes()}, commands),
		Runs:     types.ListNull(types.ObjectType{AttrTypes: runOperationAttrTypes()}),
	}
}

func commandObject(key, cmd string, sensitive bool) attr.Value {
	return types.ObjectValueMust(commandOperationAttrTypes(), map[string]attr.Value{
		"key": types.StringValue(key),
		"command": types.ListValueMust(types.StringType, []attr.Value{
			types.StringValue("sh"), types.StringValue("-c"), types.StringValue(cmd),
		}),
		"index": types.Int64Value(0), "sensitive": types.BoolValue(sensitive),
		"sudo": types.BoolValue(false), "timeout": types.StringNull(),
	})
}

func TestOpsOutputLimit(t *testing.T) {
	server := testsupport.Start(t)
	t.Setenv(outputLimitEnv, "16")
	model := newOpsModel(t, server, commandObject("big", "printf '0123456789abcdefghij'", false))
	values, sensitive, diags := model.Execute(context.Background())
	if !diags.HasError() {
		t.Fatalf("expected output limit error, got values=%v sensitive=%v", values, sensitive)
	}
	if !strings.Contains(diags[0].Detail(), "limit") {
		t.Fatalf("diagnostic = %q", diags[0].Detail())
	}
}

func TestOpsSensitiveStderrSuppressed(t *testing.T) {
	server := testsupport.Start(t)
	model := newOpsModel(t, server, commandObject("secret", "echo supersecret >&2; exit 7", true))
	_, _, diags := model.Execute(context.Background())
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
	model := newOpsModel(t, server, commandObject("plain", "echo diagnostic >&2; exit 3", false))
	_, _, diags := model.Execute(context.Background())
	if !diags.HasError() {
		t.Fatal("expected command failure")
	}
	if !strings.Contains(diags[0].Detail(), "diagnostic") {
		t.Fatalf("bounded stderr missing: %q", diags[0].Detail())
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
			"index": types.Int64Value(0), "sensitive": types.BoolValue(false),
			"sudo": types.BoolValue(false), "timeout": types.StringNull(),
		}),
	})
	_, _, diags := model.Execute(context.Background())
	if !diags.HasError() {
		t.Fatal("expected special file rejection")
	}
	if !strings.Contains(diags[0].Detail(), "not a regular file") {
		t.Fatalf("diagnostic = %q", diags[0].Detail())
	}
}
