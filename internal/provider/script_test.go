package provider

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
	"github.com/geektr-cloud/terraform-provider-ssh/internal/testsupport"
)

func TestExecuteScriptUsesStdin(t *testing.T) {
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
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	ctx := context.Background()
	run := &RunOperationModel{
		Key:    types.StringValue("run"),
		Script: types.StringValue(`printf '%s:%s' "$0" "$KEY"`),
		Shell:  types.StringValue("sh"),
	}
	out, _, err := executeScript(ctx, c, run, map[string]string{"KEY": "value"}, false, 5*time.Second, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if out != "sh:value" {
		t.Fatalf("script stdout = %q", out)
	}
}

func TestOpsRunScript(t *testing.T) {
	server := testsupport.Start(t)
	host, port, _ := net.SplitHostPort(server.Addr)
	runObj := types.ObjectValueMust(runOperationAttrTypes(), map[string]attr.Value{
		"key":     types.StringValue("run"),
		"script":  types.StringValue("printf 'run data'"),
		"shell":   types.StringValue("sh"),
		"envs":    types.MapNull(types.StringType),
		"index":   types.Int64Value(0),
		"trim":    types.BoolNull(),
		"sudo":    types.BoolValue(false),
		"timeout": types.StringNull(),
	})
	model := OpsModel{
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
		Commands: types.ListNull(types.ObjectType{AttrTypes: commandOperationAttrTypes()}),
		Runs:     types.ListValueMust(types.ObjectType{AttrTypes: runOperationAttrTypes()}, []attr.Value{runObj}),
	}
	_, _, runs, dd := model.decodeOperations(context.Background())
	if dd.HasError() {
		t.Fatalf("decode: %v", dd)
	}
	if runs[0].Script.ValueString() != "printf 'run data'" {
		t.Fatalf("decoded script = %q", runs[0].Script.ValueString())
	}
	values, diags := model.Execute(context.Background(), false)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if values["run"] != "run data" {
		t.Fatalf("values = %#v", values)
	}
}
