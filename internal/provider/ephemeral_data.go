package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ ephemeral.EphemeralResource = (*sshDataEphemeralResource)(nil)

type sshDataEphemeralResource struct{}

func (e *sshDataEphemeralResource) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_data"
}

func ephemeralConnProfileAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"type":                     schema.StringAttribute{Optional: true, Description: "Target type. Only linux is supported."},
		"host":                     schema.StringAttribute{Required: true, Description: "Target host."},
		"port":                     schema.StringAttribute{Optional: true, Description: "Target port. Defaults to 22."},
		"user":                     schema.StringAttribute{Required: true, Description: "Target user."},
		"private_key":              schema.StringAttribute{Required: true, Sensitive: true, Description: "Inline private key."},
		"known_hosts":              schema.StringAttribute{Optional: true, Description: "known_hosts path."},
		"host_key":                 schema.StringAttribute{Optional: true, Description: "Inline fixed host key."},
		"insecure_ignore_host_key": schema.BoolAttribute{Optional: true, Description: "Skip host key verification."},
		"envs":                     schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Default environment variables."},
		"timeout":                  schema.StringAttribute{Optional: true, Description: "Dial budget. Defaults to 5m."},
		"retry_delay":              schema.StringAttribute{Optional: true, Description: "Retry delay. Defaults to 5s."},
		"jump": schema.SingleNestedAttribute{
			Optional:    true,
			Description: "Optional single jump host.",
			Attributes: map[string]schema.Attribute{
				"host":                     schema.StringAttribute{Required: true, Description: "Jump host."},
				"port":                     schema.StringAttribute{Optional: true, Description: "Jump port."},
				"user":                     schema.StringAttribute{Required: true, Description: "Jump user."},
				"private_key":              schema.StringAttribute{Required: true, Sensitive: true, Description: "Jump private key."},
				"known_hosts":              schema.StringAttribute{Optional: true, Description: "Jump known_hosts."},
				"host_key":                 schema.StringAttribute{Optional: true, Description: "Jump inline host key."},
				"insecure_ignore_host_key": schema.BoolAttribute{Optional: true, Description: "Skip jump verification."},
			},
		},
	}
}

func (e *sshDataEphemeralResource) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads files and executes commands over SSH without persisting the connection profile or results.",
		Attributes: map[string]schema.Attribute{
			"conn": schema.SingleNestedAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "Connection Profile used to establish a fresh connection.",
				Attributes:  ephemeralConnProfileAttributes(),
			},
			"sudo":    schema.BoolAttribute{Optional: true, Description: "Default for operations that do not override it."},
			"timeout": schema.StringAttribute{Optional: true, Description: "Default per-operation timeout. Defaults to 5m."},
			"values": schema.MapAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Non-sensitive results keyed by operation key.",
			},
			"sensitive_values": schema.MapAttribute{
				Computed:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				Description: "Sensitive results keyed by operation key.",
			},
		},
		Blocks: map[string]schema.Block{
			"file": schema.ListNestedBlock{
				Description: "Read a remote file.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"key":       schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"path":      schema.StringAttribute{Required: true, Description: "Absolute remote file path."},
					"index":     schema.Int64Attribute{Optional: true, Description: "Execution order."},
					"sensitive": schema.BoolAttribute{Optional: true, Description: "Store in sensitive_values."},
					"sudo":      schema.BoolAttribute{Optional: true, Description: "Override top-level sudo."},
					"timeout":   schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				}},
			},
			"command": schema.ListNestedBlock{
				Description: "Run a remote argv command.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"key":       schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"command":   schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "Non-empty argv."},
					"index":     schema.Int64Attribute{Optional: true, Description: "Execution order."},
					"sensitive": schema.BoolAttribute{Optional: true, Description: "Store in sensitive_values."},
					"sudo":      schema.BoolAttribute{Optional: true, Description: "Override top-level sudo."},
					"timeout":   schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				}},
			},
			"run": schema.ListNestedBlock{
				Description: "Execute a script through the configured shell, fed over stdin.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"key":       schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"script":    schema.StringAttribute{Required: true, Description: "Script body."},
					"shell":     schema.StringAttribute{Optional: true, Description: "Shell interpreter. Defaults to sh."},
					"envs":      schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Environment overrides."},
					"index":     schema.Int64Attribute{Optional: true, Description: "Execution order."},
					"sensitive": schema.BoolAttribute{Optional: true, Description: "Store in sensitive_values."},
					"sudo":      schema.BoolAttribute{Optional: true, Description: "Override top-level sudo."},
					"timeout":   schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				}},
			},
		},
	}
}

func (e *sshDataEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var model OpsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	values, sensitive, diags := model.Execute(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	model.Values, diags = ValuesMap(values)
	resp.Diagnostics.Append(diags...)
	model.SensitiveValues, diags = ValuesMap(sensitive)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.Result.Set(ctx, &model)...)
}
