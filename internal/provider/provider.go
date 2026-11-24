// Package provider implements the geektr-cloud/ssh Terraform provider.
package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ provider.Provider                   = (*SSHProvider)(nil)
	_ provider.ProviderWithFunctions      = (*SSHProvider)(nil)
	_ provider.ProviderWithValidateConfig = (*SSHProvider)(nil)
)

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

// providerModel is the provider-level partial Connection Profile.
type providerModel struct {
	Type                  types.String       `tfsdk:"type"`
	Host                  types.String       `tfsdk:"host"`
	Port                  types.String       `tfsdk:"port"`
	User                  types.String       `tfsdk:"user"`
	PrivateKey            types.String       `tfsdk:"private_key"`
	KnownHosts            types.String       `tfsdk:"known_hosts"`
	HostKey               types.String       `tfsdk:"host_key"`
	InsecureIgnoreHostKey types.Bool         `tfsdk:"insecure_ignore_host_key"`
	Envs                  types.Map          `tfsdk:"envs"`
	Timeout               types.String       `tfsdk:"timeout"`
	RetryDelay            types.String       `tfsdk:"retry_delay"`
	Jump                  *providerJumpModel `tfsdk:"jump"`
}

type providerJumpModel struct {
	Host                  types.String `tfsdk:"host"`
	Port                  types.String `tfsdk:"port"`
	User                  types.String `tfsdk:"user"`
	PrivateKey            types.String `tfsdk:"private_key"`
	KnownHosts            types.String `tfsdk:"known_hosts"`
	HostKey               types.String `tfsdk:"host_key"`
	InsecureIgnoreHostKey types.Bool   `tfsdk:"insecure_ignore_host_key"`
}

func providerJumpAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"host": schema.StringAttribute{
			Optional:    true,
			Description: "Jump host name or address.",
		},
		"port": schema.StringAttribute{
			Optional:    true,
			Description: "Jump host SSH port, as a string between 1 and 65535. Defaults to 22.",
		},
		"user": schema.StringAttribute{
			Optional:    true,
			Description: "Jump host SSH user.",
		},
		"private_key": schema.StringAttribute{
			Optional:    true,
			Sensitive:   true,
			Description: "Inline private key for the jump host. It is not persisted by consumers unless it appears in their own `conn` JSON.",
		},
		"known_hosts": schema.StringAttribute{
			Optional:    true,
			Description: "known_hosts file for the jump host.",
		},
		"host_key": schema.StringAttribute{
			Optional:    true,
			Description: "Inline fixed host key for the jump host.",
		},
		"insecure_ignore_host_key": schema.BoolAttribute{
			Optional:    true,
			Description: "Skip jump host key verification.",
		},
	}
}

// Schema declares provider-level Connection Defaults. All fields are optional:
// consumers can rely on the built-in defaults or override individual fields.
func (p *SSHProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage remote files and directories over SSH, and read files or run commands. The provider block supplies optional connection defaults shared by every consumer.",
		Attributes: map[string]schema.Attribute{
			"type": schema.StringAttribute{
				Optional:    true,
				Description: "Target type. The only supported value is `linux`. Defaults to `linux`.",
			},
			"host": schema.StringAttribute{
				Optional:    true,
				Description: "Default target host.",
			},
			"port": schema.StringAttribute{
				Optional:    true,
				Description: "Default SSH port, as a string between 1 and 65535. Defaults to 22.",
			},
			"user": schema.StringAttribute{
				Optional:    true,
				Description: "Default SSH user.",
			},
			"private_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Default inline private key. It is not persisted in consumer state; consumers that put `private_key` in their own `conn` JSON still persist that copy.",
			},
			"known_hosts": schema.StringAttribute{
				Optional:    true,
				Description: "Default known_hosts file path. `~` is expanded; the file must exist when a connection is opened.",
			},
			"host_key": schema.StringAttribute{
				Optional:    true,
				Description: "Default inline fixed SSH host public key in authorized-key format.",
			},
			"insecure_ignore_host_key": schema.BoolAttribute{
				Optional:    true,
				Description: "Explicitly skip host key verification for default connections. Never enable this without understanding the risk.",
			},
			"envs": schema.MapAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "Default environment variables for command and run operations. Consumers merge their own `envs` by key.",
			},
			"timeout": schema.StringAttribute{
				Optional:    true,
				Description: "Default dial and retry budget, including the jump connection. Defaults to 5m.",
			},
			"retry_delay": schema.StringAttribute{
				Optional:    true,
				Description: "Default delay between retries for transient network failures. Defaults to 5s.",
			},
			"jump": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Optional default single jump host. A consumer `jump` replaces this profile as a whole.",
				Attributes:  providerJumpAttributes(),
			},
		},
	}
}

func providerStringField(v types.String, name string, strict bool, diags *diag.Diagnostics) stringField {
	if v.IsUnknown() {
		if strict {
			diags.AddError("Unknown provider connection value", fmt.Sprintf("`%s` is not known during provider configuration.", name))
		}
		return stringField{}
	}
	if v.IsNull() {
		return stringField{}
	}
	return stringValue(v.ValueString())
}

func providerBoolField(v types.Bool, name string, strict bool, diags *diag.Diagnostics) boolField {
	if v.IsUnknown() {
		if strict {
			diags.AddError("Unknown provider connection value", fmt.Sprintf("`%s` is not known during provider configuration.", name))
		}
		return boolField{}
	}
	if v.IsNull() {
		return boolField{}
	}
	return boolValue(v.ValueBool())
}

// profileFromProviderConfig converts the provider configuration into a partial
// Connection Profile. Only values explicitly present are set. When strict is
// false, unknown values are skipped so ValidateConfig can run before input
// variables are known.
func profileFromProviderConfig(m *providerModel, strict bool) (Profile, diag.Diagnostics) {
	var diags diag.Diagnostics
	profile := Profile{
		Type:                  providerStringField(m.Type, "type", strict, &diags),
		Host:                  providerStringField(m.Host, "host", strict, &diags),
		Port:                  providerStringField(m.Port, "port", strict, &diags),
		User:                  providerStringField(m.User, "user", strict, &diags),
		PrivateKey:            providerStringField(m.PrivateKey, "private_key", strict, &diags),
		KnownHosts:            providerStringField(m.KnownHosts, "known_hosts", strict, &diags),
		HostKey:               providerStringField(m.HostKey, "host_key", strict, &diags),
		InsecureIgnoreHostKey: providerBoolField(m.InsecureIgnoreHostKey, "insecure_ignore_host_key", strict, &diags),
		Timeout:               providerStringField(m.Timeout, "timeout", strict, &diags),
		RetryDelay:            providerStringField(m.RetryDelay, "retry_delay", strict, &diags),
	}
	if m.Envs.IsUnknown() {
		if strict {
			diags.AddError("Unknown provider connection value", "`envs` is not known during provider configuration.")
		}
	} else if !m.Envs.IsNull() {
		envs := map[string]string{}
		envDiags := m.Envs.ElementsAs(context.Background(), &envs, false)
		if envDiags.HasError() {
			if strict {
				diags.Append(envDiags...)
			}
		} else {
			profile.Envs = envField{Set: true, Values: envs}
		}
	}
	if m.Jump != nil {
		jump := JumpProfile{
			Host:                  providerStringField(m.Jump.Host, "jump.host", strict, &diags),
			Port:                  providerStringField(m.Jump.Port, "jump.port", strict, &diags),
			User:                  providerStringField(m.Jump.User, "jump.user", strict, &diags),
			PrivateKey:            providerStringField(m.Jump.PrivateKey, "jump.private_key", strict, &diags),
			KnownHosts:            providerStringField(m.Jump.KnownHosts, "jump.known_hosts", strict, &diags),
			HostKey:               providerStringField(m.Jump.HostKey, "jump.host_key", strict, &diags),
			InsecureIgnoreHostKey: providerBoolField(m.Jump.InsecureIgnoreHostKey, "jump.insecure_ignore_host_key", strict, &diags),
		}
		profile.Jump = jumpField{Set: true, Value: jump}
	}
	return profile, diags
}

// ValidateConfig performs configuration-only checks on the provider defaults.
func (p *SSHProvider) ValidateConfig(ctx context.Context, req provider.ValidateConfigRequest, resp *provider.ValidateConfigResponse) {
	var model providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	profile, diags := profileFromProviderConfig(&model, false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateProfile(profile, "provider"); err != nil {
		resp.Diagnostics.AddError("Invalid provider connection defaults", err.Error())
	}
}

// Configure performs no network I/O. It stores the provider defaults for every
// resource, data source, and ephemeral resource.
func (p *SSHProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var model providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	profile, diags := profileFromProviderConfig(&model, true)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateProfile(profile, "provider"); err != nil {
		resp.Diagnostics.AddError("Invalid provider connection defaults", err.Error())
		return
	}
	defaults := &providerDefaults{profile: profile}
	resp.ResourceData = defaults
	resp.DataSourceData = defaults
	resp.EphemeralResourceData = defaults
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
		func() datasource.DataSource { return &sshDataDataSource{} },
		func() datasource.DataSource { return &sshSensitiveDataDataSource{} },
	}
}

// EphemeralResources lists ephemeral resources.
func (p *SSHProvider) EphemeralResources(_ context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		func() ephemeral.EphemeralResource { return &sshDataEphemeralResource{} },
	}
}

// Functions lists provider functions.
func (p *SSHProvider) Functions(_ context.Context) []func() function.Function {
	return []func() function.Function{
		func() function.Function { return &connectionFunction{} },
	}
}
