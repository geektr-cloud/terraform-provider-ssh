package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
)

func TestValidateManagedPath(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{"/tmp/file", true},
		{"/tmp", true},
		{"tmp/file", false},
		{"/", false},
		{"/tmp/", false},
		{"/tmp//file", false},
		{"/tmp/../file", false},
		{"", false},
	}
	for _, tc := range tests {
		err := ValidateManagedPath(tc.path)
		if tc.ok && err != nil {
			t.Errorf("%q: unexpected error %v", tc.path, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%q: expected error", tc.path)
		}
	}
}

func TestParseModeAndUmask(t *testing.T) {
	if m, err := parseMode("0750"); err != nil || m.Perm() != 0750 {
		t.Fatalf("parseMode: %v %v", m, err)
	}
	for _, bad := range []string{"", "888", "10000", "-1"} {
		if _, err := parseMode(bad); err == nil {
			t.Errorf("parseMode(%q) expected error", bad)
		}
	}
	for _, bad := range []string{"888", "1000"} {
		if _, err := parseUmask(types.StringValue(bad)); err == nil {
			t.Errorf("parseUmask(%q) expected error", bad)
		}
	}
}

func TestMaxOutputSize(t *testing.T) {
	t.Setenv(outputLimitEnv, "")
	if n, err := maxOutputSize(); err != nil || n != client.DefaultMaxOutput {
		t.Fatalf("default = %d, %v", n, err)
	}
	t.Setenv(outputLimitEnv, "1024")
	if n, err := maxOutputSize(); err != nil || n != 1024 {
		t.Fatalf("override = %d, %v", n, err)
	}
	t.Setenv(outputLimitEnv, "0")
	if _, err := maxOutputSize(); err == nil {
		t.Fatal("expected invalid limit error")
	}
}

func TestValidateShell(t *testing.T) {
	for _, ok := range []string{"sh", "bash", "/bin/sh"} {
		if err := ValidateShell(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-sh", "sh -c", "sh;id"} {
		if err := ValidateShell(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestOpsValidationRejectsDuplicateKeys(t *testing.T) {
	ctx := context.Background()
	fileType := types.ObjectType{AttrTypes: fileOperationAttrTypes()}
	cmdType := types.ObjectType{AttrTypes: commandOperationAttrTypes()}
	files := types.ListValueMust(fileType, []attr.Value{
		types.ObjectValueMust(fileOperationAttrTypes(), map[string]attr.Value{
			"key": types.StringValue("dup"), "path": types.StringValue("/tmp/a"),
			"index": types.Int64Value(0), "sensitive": types.BoolValue(false),
			"sudo": types.BoolValue(false), "timeout": types.StringNull(),
		}),
	})
	commands := types.ListValueMust(cmdType, []attr.Value{
		types.ObjectValueMust(commandOperationAttrTypes(), map[string]attr.Value{
			"key":     types.StringValue("dup"),
			"command": types.ListValueMust(types.StringType, []attr.Value{types.StringValue("true")}),
			"index":   types.Int64Value(0), "sensitive": types.BoolValue(false),
			"sudo": types.BoolValue(false), "timeout": types.StringNull(),
		}),
	})
	model := OpsModel{
		Conn:     testConnModel(),
		Sudo:     types.BoolValue(false),
		Timeout:  types.StringValue("5m"),
		Files:    files,
		Commands: commands,
		Runs:     types.ListNull(types.ObjectType{AttrTypes: runOperationAttrTypes()}),
	}
	_, _, diags := model.Validate(ctx)
	if !diags.HasError() {
		t.Fatal("expected duplicate key diagnostic")
	}
}

func TestOpsValidationRejectsEmptyCommand(t *testing.T) {
	ctx := context.Background()
	cmdType := types.ObjectType{AttrTypes: commandOperationAttrTypes()}
	commands := types.ListValueMust(cmdType, []attr.Value{
		types.ObjectValueMust(commandOperationAttrTypes(), map[string]attr.Value{
			"key":     types.StringValue("k"),
			"command": types.ListValueMust(types.StringType, []attr.Value{}),
			"index":   types.Int64Value(0), "sensitive": types.BoolValue(false),
			"sudo": types.BoolValue(false), "timeout": types.StringNull(),
		}),
	})
	model := OpsModel{
		Conn:     testConnModel(),
		Sudo:     types.BoolValue(false),
		Timeout:  types.StringValue("5m"),
		Files:    types.ListNull(types.ObjectType{AttrTypes: fileOperationAttrTypes()}),
		Commands: commands,
		Runs:     types.ListNull(types.ObjectType{AttrTypes: runOperationAttrTypes()}),
	}
	_, _, diags := model.Validate(ctx)
	if !diags.HasError() {
		t.Fatal("expected empty command diagnostic")
	}
}

func TestToClientConfigDefaultsAndUnknown(t *testing.T) {
	ctx := context.Background()
	m := testConnModel()
	cfg, diags := toClientConfig(ctx, m)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.Port != "22" || cfg.Timeout.String() != "5m0s" || cfg.RetryDelay.String() != "5s" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if !cfg.InsecureIgnoreHostKey {
		t.Fatal("explicit insecure mode was not preserved")
	}

	m.Host = types.StringUnknown()
	if _, diags := toClientConfig(ctx, m); !diags.HasError() {
		t.Fatal("expected unknown host diagnostic")
	}
}

func TestToClientConfigDefaultsNullPorts(t *testing.T) {
	ctx := context.Background()
	m := testConnModel()
	m.Port = types.StringNull()
	cfg, diags := toClientConfig(ctx, m)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.Port != client.DefaultPort {
		t.Fatalf("target port = %q", cfg.Port)
	}

	m = testConnModel()
	m.Jump = &JumpModel{
		Host:                  types.StringValue("jump.example"),
		Port:                  types.StringNull(),
		User:                  types.StringValue("jumpuser"),
		PrivateKey:            types.StringValue("dummy"),
		KnownHosts:            types.StringNull(),
		HostKey:               types.StringNull(),
		InsecureIgnoreHostKey: types.BoolValue(true),
	}
	cfg, diags = toClientConfig(ctx, m)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if cfg.Jump == nil || cfg.Jump.Port != client.DefaultPort {
		t.Fatalf("jump = %+v", cfg.Jump)
	}
}

func TestDescribeOpErrorSensitivity(t *testing.T) {
	err := errors.New("boom")
	res := &client.ExecResult{Stderr: []byte("diagnostic detail")}
	if got := describeOpError(err, false, res).Error(); !strings.Contains(got, "diagnostic detail") {
		t.Fatalf("non-sensitive error = %q", got)
	}
	if got := describeOpError(err, true, res).Error(); strings.Contains(got, "diagnostic detail") {
		t.Fatalf("sensitive error leaked stderr: %q", got)
	}
}

func TestDurationFromRejectsUnknown(t *testing.T) {
	if _, err := durationFrom(types.StringUnknown(), "5m", "timeout"); err == nil {
		t.Fatal("expected unknown duration error")
	}
	if _, err := durationFrom(types.StringNull(), "5m", "timeout"); err != nil {
		t.Fatalf("null duration must use the default: %v", err)
	}
}

func testConnModel() *ConnectionModel {
	return &ConnectionModel{
		Type:                  types.StringValue("linux"),
		Host:                  types.StringValue("127.0.0.1"),
		Port:                  types.StringValue("22"),
		User:                  types.StringValue("tester"),
		PrivateKey:            types.StringValue("dummy"),
		KnownHosts:            types.StringNull(),
		HostKey:               types.StringNull(),
		InsecureIgnoreHostKey: types.BoolValue(true),
		Envs:                  types.MapValueMust(types.StringType, map[string]attr.Value{}),
		Timeout:               types.StringValue("5m"),
		RetryDelay:            types.StringValue("5s"),
	}
}
