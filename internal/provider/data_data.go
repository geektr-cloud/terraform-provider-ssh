package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource = (*sshDataDataSource)(nil)
	_ datasource.DataSource = (*sshSensitiveDataDataSource)(nil)
)

// sshDataDataSource exposes results that are not marked sensitive.
type sshDataDataSource struct{}

// sshSensitiveDataDataSource marks the whole result map sensitive and
// suppresses remote stderr in diagnostics.
type sshSensitiveDataDataSource struct{}

func (d *sshDataDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_data"
}

func (d *sshSensitiveDataDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_sensitive_data"
}

func dataOpsAttributes(valuesSensitive bool) map[string]schema.Attribute {
	valuesDescription := "Results keyed by operation key."
	if valuesSensitive {
		valuesDescription = "Results keyed by operation key. Marked sensitive, but still persisted by this data source: Sensitive is display redaction, not encryption."
	}
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
			Sensitive:   valuesSensitive,
			ElementType: types.StringType,
			Description: valuesDescription,
		},
	}
}

func trimAttribute() schema.BoolAttribute {
	return schema.BoolAttribute{
		Optional:    true,
		Computed:    true,
		Description: "Trim leading and trailing Unicode whitespace from the result. Defaults to true. Set to false to preserve the exact output, including trailing newlines.",
	}
}

func dataOpsBlocks() map[string]schema.Block {
	return map[string]schema.Block{
		"file": schema.ListNestedBlock{
			Description: "Read a remote file.",
			NestedObject: schema.NestedBlockObject{
				Attributes: map[string]schema.Attribute{
					"key":     schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"path":    schema.StringAttribute{Required: true, Description: "Absolute remote file path."},
					"index":   schema.Int64Attribute{Optional: true, Description: "Execution order. Defaults to 0."},
					"trim":    trimAttribute(),
					"sudo":    schema.BoolAttribute{Optional: true, Description: "Override the top-level sudo setting."},
					"timeout": schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				},
			},
		},
		"command": schema.ListNestedBlock{
			Description: "Run a remote argv command.",
			NestedObject: schema.NestedBlockObject{
				Attributes: map[string]schema.Attribute{
					"key":     schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"command": schema.ListAttribute{Required: true, ElementType: types.StringType, Description: "Non-empty argv. Arguments are shell-quoted, not interpreted."},
					"index":   schema.Int64Attribute{Optional: true, Description: "Execution order. Defaults to 0."},
					"trim":    trimAttribute(),
					"sudo":    schema.BoolAttribute{Optional: true, Description: "Override the top-level sudo setting."},
					"timeout": schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				},
			},
		},
		"run": schema.ListNestedBlock{
			Description: "Execute a script through the configured shell, fed over stdin.",
			NestedObject: schema.NestedBlockObject{
				Attributes: map[string]schema.Attribute{
					"key":     schema.StringAttribute{Required: true, Description: "Globally unique result key."},
					"script":  schema.StringAttribute{Required: true, Description: "Script body."},
					"shell":   schema.StringAttribute{Optional: true, Description: "Script interpreter. Defaults to sh."},
					"envs":    schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Environment overrides merged over conn.envs."},
					"index":   schema.Int64Attribute{Optional: true, Description: "Execution order. Defaults to 0."},
					"trim":    trimAttribute(),
					"sudo":    schema.BoolAttribute{Optional: true, Description: "Override the top-level sudo setting."},
					"timeout": schema.StringAttribute{Optional: true, Description: "Per-operation timeout."},
				},
			},
		},
	}
}

func dataOpsSchema(valuesSensitive bool, description string) schema.Schema {
	return schema.Schema{
		Description: description,
		Attributes:  dataOpsAttributes(valuesSensitive),
		Blocks:      dataOpsBlocks(),
	}
}

func (d *sshDataDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dataOpsSchema(false, "Reads files and executes commands over SSH. Results are persisted in values.")
}

func (d *sshSensitiveDataDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dataOpsSchema(true, "Reads files and executes commands over SSH. Every result is marked sensitive and error details suppress remote stderr. Sensitive is display redaction, not encryption: results are persisted by this data source.")
}

func (d *sshDataDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	readOpsData(ctx, req, resp, false)
}

func (d *sshSensitiveDataDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	readOpsData(ctx, req, resp, true)
}

func readOpsData(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse, sensitive bool) {
	var model OpsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	values, diags := model.Execute(ctx, sensitive)
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
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
