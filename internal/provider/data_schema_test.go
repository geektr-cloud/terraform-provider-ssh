package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	datasourceschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	ephemeralschema "github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
)

func TestDataSourcesValuesSensitivity(t *testing.T) {
	cases := []struct {
		name   string
		source datasource.DataSource
		want   bool
	}{
		{name: "ssh_data", source: &sshDataDataSource{}, want: false},
		{name: "ssh_sensitive_data", source: &sshSensitiveDataDataSource{}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp datasource.SchemaResponse
			tc.source.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
			}
			values, ok := resp.Schema.Attributes["values"].(datasourceschema.MapAttribute)
			if !ok {
				t.Fatalf("values is %T, want schema.MapAttribute", resp.Schema.Attributes["values"])
			}
			if values.Sensitive != tc.want {
				t.Fatalf("values.Sensitive = %t, want %t", values.Sensitive, tc.want)
			}
			if _, ok := resp.Schema.Attributes["sensitive_values"]; ok {
				t.Fatal("sensitive_values attribute must not exist")
			}
			for name, block := range resp.Schema.Blocks {
				list, ok := block.(datasourceschema.ListNestedBlock)
				if !ok {
					t.Fatalf("block %q is %T, want ListNestedBlock", name, block)
				}
				if _, ok := list.NestedObject.Attributes["sensitive"]; ok {
					t.Fatalf("block %q must not expose a sensitive flag", name)
				}
				trim, ok := list.NestedObject.Attributes["trim"].(datasourceschema.BoolAttribute)
				if !ok || !trim.Optional {
					t.Fatalf("block %q must expose an optional trim attribute", name)
				}
			}
		})
	}
}

func TestEphemeralValuesSensitivity(t *testing.T) {
	var resp ephemeral.SchemaResponse
	(&sshDataEphemeralResource{}).Schema(context.Background(), ephemeral.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	values, ok := resp.Schema.Attributes["values"].(ephemeralschema.MapAttribute)
	if !ok {
		t.Fatalf("values is %T, want schema.MapAttribute", resp.Schema.Attributes["values"])
	}
	if !values.Sensitive {
		t.Fatal("ephemeral values must be statically sensitive for display")
	}
	if _, ok := resp.Schema.Attributes["sensitive_values"]; ok {
		t.Fatal("sensitive_values attribute must not exist")
	}
	for name, block := range resp.Schema.Blocks {
		list, ok := block.(ephemeralschema.ListNestedBlock)
		if !ok {
			t.Fatalf("block %q is %T, want ListNestedBlock", name, block)
		}
		if _, ok := list.NestedObject.Attributes["sensitive"]; ok {
			t.Fatalf("block %q must not expose a sensitive flag", name)
		}
		trim, ok := list.NestedObject.Attributes["trim"].(ephemeralschema.BoolAttribute)
		if !ok || !trim.Optional {
			t.Fatalf("block %q must expose an optional trim attribute", name)
		}
	}
}

func TestProviderRegistersDataSources(t *testing.T) {
	p := New("test")()
	want := map[string]bool{"ssh_conn": false, "ssh_data": false, "ssh_sensitive_data": false}
	for _, factory := range p.DataSources(context.Background()) {
		var resp datasource.MetadataResponse
		factory().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "ssh"}, &resp)
		if _, ok := want[resp.TypeName]; !ok {
			t.Fatalf("unexpected data source %q", resp.TypeName)
		}
		want[resp.TypeName] = true
	}
	for name, found := range want {
		if !found {
			t.Fatalf("data source %q is not registered", name)
		}
	}
}
