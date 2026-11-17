package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = (*sshDataDataSource)(nil)

type sshDataDataSource struct{}

func (d *sshDataDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_data"
}

func dataOpsAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"conn": schema.SingleNestedAttribute{
			Required:    true,
			Sensitive:   true,
			Description: "Connection Profile used to establish a fresh connection for this read.",
			Attributes:  connDataSourceProfileAttributes(),
		},
		"sudo": schema.BoolAttribute{
			Optional:    true,
			Description: "Default for operations that do not set sudo. All sudo calls use `sudo -n`.",
		},
		"timeout": schema.StringAttribute{
			Optional:    true,
			Description: "Default per-operation timeout. Defaults to 5m.",
		},
		"values": schema.MapAttribute{
			Computed:    true,
			ElementType: types.StringType,
			Description: "Non-sensitive results keyed by operation key.",
		},
		"sensitive_values": schema.MapAttribute{
			Computed:    true,
			Sensitive:   true,
			ElementType: types.StringType,
			Description: "Sensitive results keyed by operation key. Persisted by ordinary data sources.",
		},
	}
}

func dataOpsBlocks() map[string]schema.Block {
	return map[string]schema.Block{
		"file": schema.ListNestedBlock{
			Description: "Read a remote file.",
			NestedObject: schema.NestedBlockObject{
				Attributes: map[string]schema.Attribute{
					"key":       schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"path":      schema.StringAttribute{Required: true, Description: "Absolute remote file path."},
					"index":     schema.Int64Attribute{Optional: true, Description: "Execution order. Defaults to 0."},
					"sensitive": schema.BoolAttribute{Optional: true, Description: "Store the result in sensitive_values."},
					"sudo":      schema.BoolAttribute{Optional: true, Description: "Override the top-level sudo setting."},
					"timeout":   schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				},
			},
		},
		"command": schema.ListNestedBlock{
			Description: "Run a remote argv command.",
			NestedObject: schema.NestedBlockObject{
				Attributes: map[string]schema.Attribute{
					"key":       schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"command":   schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "Non-empty argv. Arguments are shell-quoted, not interpreted."},
					"index":     schema.Int64Attribute{Optional: true, Description: "Execution order. Defaults to 0."},
					"sensitive": schema.BoolAttribute{Optional: true, Description: "Store the result in sensitive_values."},
					"sudo":      schema.BoolAttribute{Optional: true, Description: "Override the top-level sudo setting."},
					"timeout":   schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				},
			},
		},
		"run": schema.ListNestedBlock{
			Description: "Execute a script through the configured shell, fed over stdin.",
			NestedObject: schema.NestedBlockObject{
				Attributes: map[string]schema.Attribute{
					"key":       schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"script":    schema.StringAttribute{Required: true, Description: "Script body."},
					"shell":     schema.StringAttribute{Optional: true, Description: "Script interpreter. Defaults to sh."},
					"envs":      schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Environment overrides merged over conn.envs."},
					"index":     schema.Int64Attribute{Optional: true, Description: "Execution order. Defaults to 0."},
					"sensitive": schema.BoolAttribute{Optional: true, Description: "Store the result in sensitive_values."},
					"sudo":      schema.BoolAttribute{Optional: true, Description: "Override the top-level sudo setting."},
					"timeout":   schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				},
			},
		},
	}
}

func (d *sshDataDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads files and executes commands over SSH. Results are persisted according to sensitivity.",
		Attributes:  dataOpsAttributes(),
		Blocks:      dataOpsBlocks(),
	}
}

func (d *sshDataDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
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
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
