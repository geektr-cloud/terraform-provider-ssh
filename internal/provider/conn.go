package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
)

// ConnectionModel is the Terraform representation of a Connection Profile.
type ConnectionModel struct {
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
}

// JumpModel is the single-layer jump host profile.
type JumpModel struct {
	Host                  types.String `tfsdk:"host"`
	Port                  types.String `tfsdk:"port"`
	User                  types.String `tfsdk:"user"`
	PrivateKey            types.String `tfsdk:"private_key"`
	KnownHosts            types.String `tfsdk:"known_hosts"`
	HostKey               types.String `tfsdk:"host_key"`
	InsecureIgnoreHostKey types.Bool   `tfsdk:"insecure_ignore_host_key"`
}

func jumpAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"host":                     types.StringType,
		"port":                     types.StringType,
		"user":                     types.StringType,
		"private_key":              types.StringType,
		"known_hosts":              types.StringType,
		"host_key":                 types.StringType,
		"insecure_ignore_host_key": types.BoolType,
	}
}

// connectionAttrTypes returns the object type used for conn values.
func connectionAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"type":                     types.StringType,
		"host":                     types.StringType,
		"port":                     types.StringType,
		"user":                     types.StringType,
		"private_key":              types.StringType,
		"known_hosts":              types.StringType,
		"host_key":                 types.StringType,
		"insecure_ignore_host_key": types.BoolType,
		"envs":                     types.MapType{ElemType: types.StringType},
		"timeout":                  types.StringType,
		"retry_delay":              types.StringType,
		"jump":                     types.ObjectType{AttrTypes: jumpAttrTypes()},
	}
}

// connInputAttributes is used where the practitioner supplies a Connection
// Profile (the conn argument of ssh_resource and ssh_data).
func connInputAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"type": schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Default:     stringdefault.StaticString("linux"),
			Description: "Target type. The only supported value is `linux`.",
		},
		"host": schema.StringAttribute{
			Required:    true,
			Description: "Hostname or address of the target host.",
		},
		"port": schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Default:     stringdefault.StaticString(client.DefaultPort),
			Description: "SSH port, as a string between 1 and 65535.",
		},
		"user": schema.StringAttribute{
			Required:    true,
			Description: "SSH user name.",
		},
		"private_key": schema.StringAttribute{
			Required:    true,
			Sensitive:   true,
			Description: "Inline PEM or OpenSSH private key. Persisted in state by design.",
		},
		"known_hosts": schema.StringAttribute{
			Optional:    true,
			Description: "Path to a known_hosts file. `~` is expanded; the file must exist.",
		},
		"host_key": schema.StringAttribute{
			Optional:    true,
			Description: "Inline fixed SSH host public key in authorized-key format.",
		},
		"insecure_ignore_host_key": schema.BoolAttribute{
			Optional:    true,
			Computed:    true,
			Default:     booldefault.StaticBool(false),
			Description: "Explicitly skip host key verification. Never enable this without understanding the risk.",
		},
		"envs": schema.MapAttribute{
			ElementType: types.StringType,
			Optional:    true,
			Computed:    true,
			Default:     mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{})),
			Description: "Default environment variables for command and run operations.",
		},
		"timeout": schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Default:     stringdefault.StaticString("5m"),
			Description: "Dial and retry budget, including the jump connection.",
		},
		"retry_delay": schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Default:     stringdefault.StaticString("5s"),
			Description: "Delay between retries for transient network failures.",
		},
		"jump": schema.SingleNestedAttribute{
			Optional:    true,
			Description: "Optional single jump host. It cannot contain another jump host.",
			Attributes: map[string]schema.Attribute{
				"host": schema.StringAttribute{
					Required:    true,
					Description: "Jump host name or address.",
				},
				"port": schema.StringAttribute{
					Optional:    true,
					Computed:    true,
					Default:     stringdefault.StaticString(client.DefaultPort),
					Description: "Jump host SSH port.",
				},
				"user": schema.StringAttribute{
					Required:    true,
					Description: "Jump host SSH user.",
				},
				"private_key": schema.StringAttribute{
					Required:    true,
					Sensitive:   true,
					Description: "Inline private key for the jump host.",
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
					Computed:    true,
					Default:     booldefault.StaticBool(false),
					Description: "Skip jump host key verification.",
				},
			},
		},
	}
}

func connObjectValue(_ context.Context, m *ConnectionModel) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	envs := m.Envs
	if envs.IsNull() || envs.IsUnknown() {
		envs = types.MapValueMust(types.StringType, map[string]attr.Value{})
	}
	var jump attr.Value = types.ObjectNull(jumpAttrTypes())
	if m.Jump != nil {
		jumpObj, d := types.ObjectValue(jumpAttrTypes(), map[string]attr.Value{
			"host":                     m.Jump.Host,
			"port":                     m.Jump.Port,
			"user":                     m.Jump.User,
			"private_key":              m.Jump.PrivateKey,
			"known_hosts":              m.Jump.KnownHosts,
			"host_key":                 m.Jump.HostKey,
			"insecure_ignore_host_key": m.Jump.InsecureIgnoreHostKey,
		})
		diags.Append(d...)
		jump = jumpObj
	}
	return types.ObjectValue(connectionAttrTypes(), map[string]attr.Value{
		"type":                     m.Type,
		"host":                     m.Host,
		"port":                     m.Port,
		"user":                     m.User,
		"private_key":              m.PrivateKey,
		"known_hosts":              m.KnownHosts,
		"host_key":                 m.HostKey,
		"insecure_ignore_host_key": m.InsecureIgnoreHostKey,
		"envs":                     envs,
		"timeout":                  m.Timeout,
		"retry_delay":              m.RetryDelay,
		"jump":                     jump,
	})
}

// toClientConfig validates and converts a Terraform Connection Profile into
// the internal client configuration. It rejects unknown and null required
// values.
func toClientConfig(ctx context.Context, m *ConnectionModel) (client.Config, diag.Diagnostics) {
	var diags diag.Diagnostics
	if m == nil {
		diags.AddError("Missing connection profile", "A `conn` Connection Profile is required.")
		return client.Config{}, diags
	}

	get := func(v types.String, name string, required bool) (string, bool) {
		if v.IsUnknown() {
			diags.AddError("Unknown connection value", fmt.Sprintf("`%s` is not known during this operation.", name))
			return "", false
		}
		if v.IsNull() || (required && strings.TrimSpace(v.ValueString()) == "") {
			if required {
				diags.AddError("Missing connection value", fmt.Sprintf("`%s` is required.", name))
				return "", false
			}
			return "", true
		}
		return v.ValueString(), true
	}

	host, ok := get(m.Host, "host", true)
	if !ok {
		return client.Config{}, diags
	}
	port, ok := get(m.Port, "port", false)
	if !ok {
		return client.Config{}, diags
	}
	if strings.TrimSpace(port) == "" {
		port = client.DefaultPort
	}
	user, ok := get(m.User, "user", true)
	if !ok {
		return client.Config{}, diags
	}
	privateKey, ok := get(m.PrivateKey, "private_key", true)
	if !ok {
		return client.Config{}, diags
	}
	knownHosts, _ := get(m.KnownHosts, "known_hosts", false)
	hostKey, _ := get(m.HostKey, "host_key", false)
	typ, _ := get(m.Type, "type", false)
	if typ == "" {
		typ = "linux"
	}
	if typ != "linux" {
		diags.AddError("Unsupported connection type", fmt.Sprintf("`type` must be `linux`, got %q.", typ))
		return client.Config{}, diags
	}
	if m.InsecureIgnoreHostKey.IsUnknown() {
		diags.AddError("Unknown connection value", "`insecure_ignore_host_key` is not known during this operation.")
		return client.Config{}, diags
	}
	timeout, ok := parseDurationValue(m.Timeout, "5m", "timeout", &diags)
	if !ok {
		return client.Config{}, diags
	}
	retryDelay, ok := parseDurationValue(m.RetryDelay, "5s", "retry_delay", &diags)
	if !ok {
		return client.Config{}, diags
	}

	envs := map[string]string{}
	if !m.Envs.IsNull() && !m.Envs.IsUnknown() {
		if d := m.Envs.ElementsAs(ctx, &envs, false); d.HasError() {
			diags.Append(d...)
			return client.Config{}, diags
		}
	} else if m.Envs.IsUnknown() {
		diags.AddError("Unknown connection value", "`envs` is not known during this operation.")
		return client.Config{}, diags
	}

	ep := client.Endpoint{
		Host:                  host,
		Port:                  port,
		User:                  user,
		PrivateKey:            privateKey,
		KnownHosts:            knownHosts,
		HostKey:               hostKey,
		InsecureIgnoreHostKey: m.InsecureIgnoreHostKey.ValueBool(),
	}

	var jump *client.Endpoint
	if m.Jump != nil {
		jHost, ok := get(m.Jump.Host, "jump.host", true)
		if !ok {
			return client.Config{}, diags
		}
		jPort, ok := get(m.Jump.Port, "jump.port", false)
		if !ok {
			return client.Config{}, diags
		}
		if strings.TrimSpace(jPort) == "" {
			jPort = client.DefaultPort
		}
		jUser, ok := get(m.Jump.User, "jump.user", true)
		if !ok {
			return client.Config{}, diags
		}
		jKey, ok := get(m.Jump.PrivateKey, "jump.private_key", true)
		if !ok {
			return client.Config{}, diags
		}
		jKnown, _ := get(m.Jump.KnownHosts, "jump.known_hosts", false)
		jHostKey, _ := get(m.Jump.HostKey, "jump.host_key", false)
		if m.Jump.InsecureIgnoreHostKey.IsUnknown() {
			diags.AddError("Unknown connection value", "`jump.insecure_ignore_host_key` is not known during this operation.")
			return client.Config{}, diags
		}
		jump = &client.Endpoint{
			Host:                  jHost,
			Port:                  jPort,
			User:                  jUser,
			PrivateKey:            jKey,
			KnownHosts:            jKnown,
			HostKey:               jHostKey,
			InsecureIgnoreHostKey: m.Jump.InsecureIgnoreHostKey.ValueBool(),
		}
	}

	cfg := client.Config{
		Endpoint:   ep,
		Envs:       envs,
		Timeout:    timeout,
		RetryDelay: retryDelay,
		Jump:       jump,
	}
	if err := cfg.Validate(); err != nil {
		diags.AddError("Invalid connection profile", err.Error())
		return client.Config{}, diags
	}
	return cfg, diags
}

func parseDurationValue(v types.String, def, name string, diags *diag.Diagnostics) (time.Duration, bool) {
	if v.IsUnknown() {
		diags.AddError("Unknown connection value", fmt.Sprintf("`%s` is not known during this operation.", name))
		return 0, false
	}
	s := def
	if !v.IsNull() && v.ValueString() != "" {
		s = v.ValueString()
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		diags.AddError("Invalid "+name, fmt.Sprintf("`%s` must be a valid duration such as `5m`: %s", name, err))
		return 0, false
	}
	if d <= 0 {
		diags.AddError("Invalid "+name, fmt.Sprintf("`%s` must be positive.", name))
		return 0, false
	}
	return d, true
}

// normalizedConnectionModel applies the documented defaults for
// Optional+Computed connection fields. It does not select a host key mode.
func normalizedConnectionModel(m *ConnectionModel) *ConnectionModel {
	out := *m
	if out.Type.IsNull() || out.Type.IsUnknown() || out.Type.ValueString() == "" {
		out.Type = types.StringValue("linux")
	}
	if out.Port.IsNull() || out.Port.IsUnknown() || out.Port.ValueString() == "" {
		out.Port = types.StringValue(client.DefaultPort)
	}
	if out.Timeout.IsNull() || out.Timeout.IsUnknown() || out.Timeout.ValueString() == "" {
		out.Timeout = types.StringValue("5m")
	}
	if out.RetryDelay.IsNull() || out.RetryDelay.IsUnknown() || out.RetryDelay.ValueString() == "" {
		out.RetryDelay = types.StringValue("5s")
	}
	if out.Envs.IsNull() || out.Envs.IsUnknown() {
		out.Envs = types.MapValueMust(types.StringType, map[string]attr.Value{})
	}
	if out.InsecureIgnoreHostKey.IsNull() || out.InsecureIgnoreHostKey.IsUnknown() {
		out.InsecureIgnoreHostKey = types.BoolValue(false)
	}
	if out.Jump != nil {
		j := *out.Jump
		if j.Port.IsNull() || j.Port.IsUnknown() || j.Port.ValueString() == "" {
			j.Port = types.StringValue(client.DefaultPort)
		}
		if j.InsecureIgnoreHostKey.IsNull() || j.InsecureIgnoreHostKey.IsUnknown() {
			j.InsecureIgnoreHostKey = types.BoolValue(false)
		}
		out.Jump = &j
	}
	return &out
}

// resolveProfile fills defaults and selects the effective host key mode for
// the value returned by data.ssh_conn.
func resolveProfile(m *ConnectionModel) *ConnectionModel {
	out := normalizedConnectionModel(m)
	if out.KnownHosts.IsNull() && out.HostKey.IsNull() && !out.InsecureIgnoreHostKey.ValueBool() {
		out.KnownHosts = types.StringValue("~/.ssh/known_hosts")
	}
	if out.Jump != nil && out.Jump.KnownHosts.IsNull() && out.Jump.HostKey.IsNull() && !out.Jump.InsecureIgnoreHostKey.ValueBool() {
		j := *out.Jump
		j.KnownHosts = types.StringValue("~/.ssh/known_hosts")
		out.Jump = &j
	}
	return out
}
