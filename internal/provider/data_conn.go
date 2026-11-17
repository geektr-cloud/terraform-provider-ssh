package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
)

var _ datasource.DataSource = (*sshConnDataSource)(nil)

type sshConnDataSource struct{}

// sshConnDataModel is the flat configuration plus the computed Connection
// Profile returned by data.ssh_conn.
type sshConnDataModel struct {
	Type                  types.String `tfsdk:"type"`
	Host                  types.String `tfsdk:"host"`
	Port                  types.String `tfsdk:"port"`
	User                  types.String `tfsdk:"user"`
	PrivateKey            types.String `tfsdk:"private_key"`
	KnownHosts            types.String `tfsdk:"known_hosts"`
	HostKey               types.String `tfsdk:"host_key"`
	InsecureIgnoreHostKey types.Bool   `tfsdk:"insecure_ignore_host_key"`
	Envs                  types.Map    `tfsdk:"envs"`
	Timeout               types.String `tfsdk:"timeout"`
	RetryDelay            types.String `tfsdk:"retry_delay"`
	Jump                  *JumpModel   `tfsdk:"jump"`
	Conn                  types.Object `tfsdk:"conn"`
}

func (m *sshConnDataModel) connection() *ConnectionModel {
	return &ConnectionModel{
		Type:                  m.Type,
		Host:                  m.Host,
		Port:                  m.Port,
		User:                  m.User,
		PrivateKey:            m.PrivateKey,
		KnownHosts:            m.KnownHosts,
		HostKey:               m.HostKey,
		InsecureIgnoreHostKey: m.InsecureIgnoreHostKey,
		Envs:                  m.Envs,
		Timeout:               m.Timeout,
		RetryDelay:            m.RetryDelay,
		Jump:                  m.Jump,
	}
}

func (m *sshConnDataModel) setConnection(c *ConnectionModel) {
	m.Type = c.Type
	m.Host = c.Host
	m.Port = c.Port
	m.User = c.User
	m.PrivateKey = c.PrivateKey
	m.KnownHosts = c.KnownHosts
	m.HostKey = c.HostKey
	m.InsecureIgnoreHostKey = c.InsecureIgnoreHostKey
	m.Envs = c.Envs
	m.Timeout = c.Timeout
	m.RetryDelay = c.RetryDelay
	m.Jump = c.Jump
}

func (d *sshConnDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_conn"
}

func connDataSourceAttributes() map[string]schema.Attribute {
	attrs := connDataSourceProfileAttributes()
	attrs["conn"] = schema.SingleNestedAttribute{
		Computed:    true,
		Sensitive:   true,
		Description: "Complete, sensitive Connection Profile. Consumers persist it and reconnect for every operation.",
		Attributes:  connDataSourceComputedAttributes(),
	}
	return attrs
}

func connDataSourceProfileAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"type": schema.StringAttribute{
			Optional:    true,
			Description: "Target type. The only supported value is `linux`.",
		},
		"host": schema.StringAttribute{
			Required:    true,
			Description: "Hostname or address of the target host.",
		},
		"port": schema.StringAttribute{
			Optional:    true,
			Description: "SSH port, as a string between 1 and 65535. Defaults to 22.",
		},
		"user": schema.StringAttribute{
			Required:    true,
			Description: "SSH user name.",
		},
		"private_key": schema.StringAttribute{
			Required:    true,
			Sensitive:   true,
			Description: "Inline private key. Persisted in state by design.",
		},
		"known_hosts": schema.StringAttribute{
			Optional:    true,
			Description: "known_hosts file path. Defaults to ~/.ssh/known_hosts.",
		},
		"host_key": schema.StringAttribute{
			Optional:    true,
			Description: "Inline fixed host public key in authorized-key format.",
		},
		"insecure_ignore_host_key": schema.BoolAttribute{
			Optional:    true,
			Description: "Explicitly skip host key verification.",
		},
		"envs": schema.MapAttribute{
			ElementType: types.StringType,
			Optional:    true,
			Description: "Default environment variables for command and run operations.",
		},
		"timeout": schema.StringAttribute{
			Optional:    true,
			Description: "Dial and retry budget. Defaults to 5m.",
		},
		"retry_delay": schema.StringAttribute{
			Optional:    true,
			Description: "Delay between retries for transient network failures. Defaults to 5s.",
		},
		"jump": schema.SingleNestedAttribute{
			Optional:    true,
			Description: "Optional single jump host.",
			Attributes: map[string]schema.Attribute{
				"host":                     schema.StringAttribute{Required: true, Description: "Jump host."},
				"port":                     schema.StringAttribute{Optional: true, Description: "Jump port."},
				"user":                     schema.StringAttribute{Required: true, Description: "Jump user."},
				"private_key":              schema.StringAttribute{Required: true, Sensitive: true, Description: "Jump private key."},
				"known_hosts":              schema.StringAttribute{Optional: true, Description: "Jump known_hosts path."},
				"host_key":                 schema.StringAttribute{Optional: true, Description: "Jump inline host key."},
				"insecure_ignore_host_key": schema.BoolAttribute{Optional: true, Description: "Skip jump host key verification."},
			},
		},
	}
}

func connDataSourceComputedAttributes() map[string]schema.Attribute {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	return map[string]schema.Attribute{
		"type":                     str("Target type."),
		"host":                     str("Target host."),
		"port":                     str("Target port."),
		"user":                     str("Target user."),
		"private_key":              schema.StringAttribute{Computed: true, Sensitive: true, Description: "Private key."},
		"known_hosts":              str("known_hosts path."),
		"host_key":                 str("Inline host key."),
		"insecure_ignore_host_key": schema.BoolAttribute{Computed: true, Description: "Whether host verification is skipped."},
		"envs":                     schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "Default environment variables."},
		"timeout":                  str("Dial budget."),
		"retry_delay":              str("Retry delay."),
		"jump": schema.SingleNestedAttribute{
			Computed:    true,
			Description: "Jump profile.",
			Attributes: map[string]schema.Attribute{
				"host":                     str("Jump host."),
				"port":                     str("Jump port."),
				"user":                     str("Jump user."),
				"private_key":              schema.StringAttribute{Computed: true, Sensitive: true, Description: "Jump private key."},
				"known_hosts":              str("Jump known_hosts."),
				"host_key":                 str("Jump host key."),
				"insecure_ignore_host_key": schema.BoolAttribute{Computed: true, Description: "Skip jump verification."},
			},
		},
	}
}

func (d *sshConnDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Performs a real SSH connection probe and returns a complete, sensitive Connection Profile.",
		Attributes:  connDataSourceAttributes(),
	}
}

func (d *sshConnDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var model sshConnDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cm := model.connection()
	cfg, diags := toClientConfig(ctx, cm)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	probe, err := client.Dial(ctx, cfg)
	if err != nil {
		resp.Diagnostics.AddError("SSH connection probe failed", err.Error())
		return
	}
	_ = probe.Close()

	resolved := resolveProfile(cm)
	obj, objDiags := connObjectValue(ctx, resolved)
	resp.Diagnostics.Append(objDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	model.setConnection(resolved)
	model.Conn = obj
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
