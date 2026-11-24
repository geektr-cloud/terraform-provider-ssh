package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ ephemeral.EphemeralResource              = (*sshDataEphemeralResource)(nil)
	_ ephemeral.EphemeralResourceWithConfigure = (*sshDataEphemeralResource)(nil)
)

type sshDataEphemeralResource struct {
	defaults *providerDefaults
}

func (e *sshDataEphemeralResource) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_data"
}

// Configure receives the provider-level Connection Defaults.
func (e *sshDataEphemeralResource) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	defaults, ok := providerDefaultsFromData(req.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "The SSH provider received an unexpected provider data type.")
		return
	}
	if defaults != nil {
		e.defaults = defaults
	}
}

func (e *sshDataEphemeralResource) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads files and executes commands over SSH without persisting the connection override or results.",
		Attributes: map[string]schema.Attribute{
			"conn": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Optional JSON Connection Override. Omit, null, or `{}` to use the provider defaults unchanged. Ephemeral values are never persisted.",
			},
			"sudo":    schema.BoolAttribute{Optional: true, Description: "Default for operations that do not override it."},
			"timeout": schema.StringAttribute{Optional: true, Description: "Default per-operation timeout. Defaults to 5m."},
			"values": schema.MapAttribute{
				Computed:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				Description: "Results keyed by operation key. Marked sensitive for display; ephemeral results are never persisted.",
			},
		},
		Blocks: map[string]schema.Block{
			"file": schema.ListNestedBlock{
				Description: "Read a remote file.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"key":     schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"path":    schema.StringAttribute{Required: true, Description: "Absolute remote file path."},
					"index":   schema.Int64Attribute{Optional: true, Description: "Execution order."},
					"trim":    schema.BoolAttribute{Optional: true, Computed: true, Description: "Trim leading and trailing Unicode whitespace from the result. Defaults to true. Set to false to preserve the exact output, including trailing newlines."},
					"sudo":    schema.BoolAttribute{Optional: true, Description: "Override top-level sudo."},
					"timeout": schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				}},
			},
			"command": schema.ListNestedBlock{
				Description: "Run a remote argv command.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"key":     schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"command": schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "Non-empty argv."},
					"index":   schema.Int64Attribute{Optional: true, Description: "Execution order."},
					"trim":    schema.BoolAttribute{Optional: true, Computed: true, Description: "Trim leading and trailing Unicode whitespace from the result. Defaults to true. Set to false to preserve the exact output, including trailing newlines."},
					"sudo":    schema.BoolAttribute{Optional: true, Description: "Override top-level sudo."},
					"timeout": schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				}},
			},
			"run": schema.ListNestedBlock{
				Description: "Execute a script through the configured shell, fed over stdin.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"key":     schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"script":  schema.StringAttribute{Required: true, Description: "Script body."},
					"shell":   schema.StringAttribute{Optional: true, Description: "Shell interpreter. Defaults to sh."},
					"envs":    schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Environment overrides."},
					"index":   schema.Int64Attribute{Optional: true, Description: "Execution order."},
					"trim":    schema.BoolAttribute{Optional: true, Computed: true, Description: "Trim leading and trailing Unicode whitespace from the result. Defaults to true. Set to false to preserve the exact output, including trailing newlines."},
					"sudo":    schema.BoolAttribute{Optional: true, Description: "Override top-level sudo."},
					"timeout": schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
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
	model.defaults = e.defaults
	values, diags := model.Execute(ctx, true)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(model.ApplyTrimDefaults()...)
	if resp.Diagnostics.HasError() {
		return
	}
	model.Values, diags = ValuesMap(values)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.Result.Set(ctx, &model)...)
}
