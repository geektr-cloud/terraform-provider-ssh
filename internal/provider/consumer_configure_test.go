package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	resourceschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

func TestResourceConnectionSchema(t *testing.T) {
	var resp resource.SchemaResponse
	(&sshResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	conn, ok := resp.Schema.Attributes["conn"].(resourceschema.StringAttribute)
	if !ok {
		t.Fatalf("conn is %T, want StringAttribute", resp.Schema.Attributes["conn"])
	}
	if !conn.Optional || !conn.Sensitive {
		t.Fatal("resource conn must be optional and sensitive")
	}
	identity, ok := resp.Schema.Attributes["connection_identity"].(resourceschema.StringAttribute)
	if !ok {
		t.Fatalf("connection_identity is %T, want StringAttribute", resp.Schema.Attributes["connection_identity"])
	}
	if !identity.Computed || identity.Sensitive {
		t.Fatal("connection_identity must be a non-sensitive computed string")
	}
}

func TestConsumersReceiveProviderDefaults(t *testing.T) {
	ctx := context.Background()
	defaults := testStaticDefaults()

	res := &sshResource{}
	var resResp resource.ConfigureResponse
	res.Configure(ctx, resource.ConfigureRequest{ProviderData: defaults}, &resResp)
	if resResp.Diagnostics.HasError() {
		t.Fatalf("resource configure: %v", resResp.Diagnostics)
	}
	if res.defaults != defaults {
		t.Fatal("resource did not receive provider defaults")
	}

	data := &sshDataDataSource{}
	var dataResp datasource.ConfigureResponse
	data.Configure(ctx, datasource.ConfigureRequest{ProviderData: defaults}, &dataResp)
	if dataResp.Diagnostics.HasError() {
		t.Fatalf("data configure: %v", dataResp.Diagnostics)
	}
	if data.defaults != defaults {
		t.Fatal("data source did not receive provider defaults")
	}

	sensitive := &sshSensitiveDataDataSource{}
	var sensitiveResp datasource.ConfigureResponse
	sensitive.Configure(ctx, datasource.ConfigureRequest{ProviderData: defaults}, &sensitiveResp)
	if sensitiveResp.Diagnostics.HasError() {
		t.Fatalf("sensitive data configure: %v", sensitiveResp.Diagnostics)
	}
	if sensitive.defaults != defaults {
		t.Fatal("sensitive data source did not receive provider defaults")
	}

	ephemeralResource := &sshDataEphemeralResource{}
	var ephemeralResp ephemeral.ConfigureResponse
	ephemeralResource.Configure(ctx, ephemeral.ConfigureRequest{ProviderData: defaults}, &ephemeralResp)
	if ephemeralResp.Diagnostics.HasError() {
		t.Fatalf("ephemeral configure: %v", ephemeralResp.Diagnostics)
	}
	if ephemeralResource.defaults != defaults {
		t.Fatal("ephemeral resource did not receive provider defaults")
	}
}

func TestConsumerConfigureRejectsWrongProviderData(t *testing.T) {
	ctx := context.Background()
	res := &sshResource{}
	var resp resource.ConfigureResponse
	res.Configure(ctx, resource.ConfigureRequest{ProviderData: "wrong"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a diagnostic for unexpected provider data")
	}

	var nilResp resource.ConfigureResponse
	res.Configure(ctx, resource.ConfigureRequest{}, &nilResp)
	if nilResp.Diagnostics.HasError() {
		t.Fatalf("unconfigured provider must not error in Configure: %v", nilResp.Diagnostics)
	}
}
