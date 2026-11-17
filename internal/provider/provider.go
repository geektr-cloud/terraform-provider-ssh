// Package provider implements the geektr-cloud/ssh Terraform provider.
package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ provider.Provider = (*SSHProvider)(nil)

// SSHProvider implements the geektr-cloud/ssh provider.
type SSHProvider struct {
	version string
}

// New returns a new provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &SSHProvider{version: version}
	}
}

// Metadata sets the provider type name and version.
func (p *SSHProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "ssh"
	resp.Version = p.version
}

// Schema has no provider-level credentials. All connection settings belong to
// a Connection Profile.
func (p *SSHProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage remote files and directories over SSH, and read files or run commands.",
	}
}

// Configure performs no network I/O.
func (p *SSHProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
}

// Resources lists managed resources.
func (p *SSHProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		func() resource.Resource { return &sshResource{} },
	}
}

// DataSources lists data sources.
func (p *SSHProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		func() datasource.DataSource { return &sshConnDataSource{} },
		func() datasource.DataSource { return &sshDataDataSource{} },
	}
}

// EphemeralResources lists ephemeral resources.
func (p *SSHProvider) EphemeralResources(_ context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		func() ephemeral.EphemeralResource { return &sshDataEphemeralResource{} },
	}
}
