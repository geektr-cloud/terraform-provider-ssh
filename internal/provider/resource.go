package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/remote"
)

var (
	_ resource.Resource               = (*sshResource)(nil)
	_ resource.ResourceWithModifyPlan = (*sshResource)(nil)
	_ resource.ResourceWithConfigure  = (*sshResource)(nil)
)

type sshResource struct {
	defaults *providerDefaults
}

// sshResourceModel is the full resource state.
type sshResourceModel struct {
	Conn               types.String `tfsdk:"conn"`
	ConnectionIdentity types.String `tfsdk:"connection_identity"`
	Umask              types.String `tfsdk:"umask"`
	Owner              types.String `tfsdk:"owner"`
	UID                types.Int64  `tfsdk:"uid"`
	Group              types.String `tfsdk:"group"`
	GID                types.Int64  `tfsdk:"gid"`
	Directories        types.List   `tfsdk:"directory"`
	Files              types.List   `tfsdk:"file"`
}

// Configure receives the provider-level Connection Defaults.
func (r *sshResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	defaults, ok := req.ProviderData.(*providerDefaults)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "The SSH provider received an unexpected provider data type.")
		return
	}
	r.defaults = defaults
}

type directoryModel struct {
	Path       types.String `tfsdk:"path"`
	Mode       types.String `tfsdk:"mode"`
	Owner      types.String `tfsdk:"owner"`
	UID        types.Int64  `tfsdk:"uid"`
	Group      types.String `tfsdk:"group"`
	GID        types.Int64  `tfsdk:"gid"`
	AutoImport types.Bool   `tfsdk:"auto_import"`
}

type fileModel struct {
	Path          types.String `tfsdk:"path"`
	Mode          types.String `tfsdk:"mode"`
	Owner         types.String `tfsdk:"owner"`
	UID           types.Int64  `tfsdk:"uid"`
	Group         types.String `tfsdk:"group"`
	GID           types.Int64  `tfsdk:"gid"`
	AutoImport    types.Bool   `tfsdk:"auto_import"`
	Content       types.String `tfsdk:"content"`
	ContentWO     types.String `tfsdk:"content_wo"`
	ContentSHA256 types.String `tfsdk:"content_sha256"`
}

func directoryAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"path":        types.StringType,
		"mode":        types.StringType,
		"owner":       types.StringType,
		"uid":         types.Int64Type,
		"group":       types.StringType,
		"gid":         types.Int64Type,
		"auto_import": types.BoolType,
	}
}

func fileAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"path":           types.StringType,
		"mode":           types.StringType,
		"owner":          types.StringType,
		"uid":            types.Int64Type,
		"group":          types.StringType,
		"gid":            types.Int64Type,
		"auto_import":    types.BoolType,
		"content":        types.StringType,
		"content_wo":     types.StringType,
		"content_sha256": types.StringType,
	}
}

func metadataAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"mode": schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Description: "Octal mode. Defaults to 0666 &^ umask for files and 0777 &^ umask for directories.",
		},
		"owner": schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Description: "Owner name. Mutually exclusive with uid within the same scope.",
		},
		"uid": schema.Int64Attribute{
			Optional:    true,
			Computed:    true,
			Description: "Numeric owner uid. Mutually exclusive with owner within the same scope.",
		},
		"group": schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Description: "Group name. Mutually exclusive with gid within the same scope.",
		},
		"gid": schema.Int64Attribute{
			Optional:    true,
			Computed:    true,
			Description: "Numeric group gid. Mutually exclusive with group within the same scope.",
		},
	}
}

func (r *sshResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_resource"
}

func (r *sshResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	dirAttrs := metadataAttributes()
	dirAttrs["path"] = schema.StringAttribute{Required: true, Description: "Cleaned absolute remote directory path."}
	dirAttrs["auto_import"] = schema.BoolAttribute{
		Optional:    true,
		Computed:    true,
		Default:     booldefault.StaticBool(false),
		Description: "Adopt an existing exact path into management.",
	}

	fileAttrs := metadataAttributes()
	fileAttrs["path"] = schema.StringAttribute{Required: true, Description: "Cleaned absolute remote file path."}
	fileAttrs["auto_import"] = schema.BoolAttribute{
		Optional:    true,
		Computed:    true,
		Default:     booldefault.StaticBool(false),
		Description: "Adopt an existing exact path into management.",
	}
	fileAttrs["content"] = schema.StringAttribute{
		Optional:    true,
		Description: "Plain file content. Mutually exclusive with content_wo. Empty string is valid.",
	}
	fileAttrs["content_wo"] = schema.StringAttribute{
		Optional:    true,
		Sensitive:   true,
		WriteOnly:   true,
		Description: "Write-only file content. Not stored in plan or state. Mutually exclusive with content.",
	}
	fileAttrs["content_sha256"] = schema.StringAttribute{
		Computed:    true,
		Description: "SHA-256 digest of content_wo. This is a content identity, not encryption.",
	}

	resp.Schema = schema.Schema{
		Description: "Manages a collection of independent remote files and directories over SSH.",
		Attributes: map[string]schema.Attribute{
			"conn": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Optional JSON Connection Override. Omit, null, or `{}` to use the provider defaults unchanged. Unknown fields, duplicate keys, arrays, scalars, and wrong value types are rejected. A private key written here is persisted in state.",
			},
			"connection_identity": schema.StringAttribute{
				Computed:    true,
				Description: "Non-sensitive canonical Connection Identity (target and jump host/port/user). A change replaces the resource.",
			},
			"umask": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("022"),
				Description: "Octal umask used to derive default modes. It is not applied to the remote process.",
			},
			"owner": schema.StringAttribute{Optional: true, Description: "Default owner name for path blocks."},
			"uid":   schema.Int64Attribute{Optional: true, Description: "Default numeric owner uid for path blocks."},
			"group": schema.StringAttribute{Optional: true, Description: "Default group name for path blocks."},
			"gid":   schema.Int64Attribute{Optional: true, Description: "Default numeric group gid for path blocks."},
		},
		Blocks: map[string]schema.Block{
			"directory": schema.ListNestedBlock{
				Description:  "Managed directory. Only the exact directory is owned; children are never modified recursively.",
				NestedObject: schema.NestedBlockObject{Attributes: dirAttrs},
			},
			"file": schema.ListNestedBlock{
				Description:  "Managed file. Content is exactly one of content or content_wo.",
				NestedObject: schema.NestedBlockObject{Attributes: fileAttrs},
			},
		},
	}
}

// ---- plan-time validation and write-only hash handling ----

func (r *sshResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan sshResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var cfgModel sshResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfgModel)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(validateResourceModel(ctx, &cfgModel, r.defaults, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfgFiles, cfgDiags := decodeFiles(ctx, cfgModel.Files)
	resp.Diagnostics.Append(cfgDiags...)
	planFiles, planDiags := decodeFiles(ctx, plan.Files)
	resp.Diagnostics.Append(planDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	byPath := make(map[string]fileModel, len(cfgFiles))
	for _, f := range cfgFiles {
		byPath[f.Path.ValueString()] = f
	}
	for i := range planFiles {
		f := &planFiles[i]
		cfgFile, ok := fileModel{}, false
		if !f.Path.IsNull() && !f.Path.IsUnknown() {
			cfgFile, ok = byPath[f.Path.ValueString()]
		}
		if !ok && i < len(cfgFiles) {
			cfgFile, ok = cfgFiles[i], true
		}
		if !ok {
			continue
		}
		switch {
		case !cfgFile.ContentWO.IsNull() && cfgFile.ContentWO.IsUnknown():
			f.ContentSHA256 = types.StringUnknown()
		case !cfgFile.ContentWO.IsNull():
			f.Content = types.StringNull()
			f.ContentWO = types.StringNull()
			f.ContentSHA256 = types.StringValue(hashString(cfgFile.ContentWO.ValueString()))
		case !cfgFile.Content.IsNull() && !cfgFile.Content.IsUnknown():
			f.Content = cfgFile.Content
			f.ContentWO = types.StringNull()
			f.ContentSHA256 = types.StringNull()
		}
	}

	list, d := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: fileAttrTypes()}, planFiles)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.Files = list

	// Compare the Connection Identity from the prior state with the identity
	// implied by the current provider defaults and configuration. Only the
	// identity (target and jump host/port/user) forces replacement.
	var prior sshResourceModel
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if !cfgModel.Conn.IsUnknown() {
		current, known, err := resolveConnIdentity(defaultsProfile(r.defaults), cfgModel.Conn)
		if err != nil {
			resp.Diagnostics.AddError("Invalid connection override", err.Error())
			return
		}
		priorIdentity := prior.ConnectionIdentity
		if known && !priorIdentity.IsNull() && !priorIdentity.IsUnknown() && priorIdentity.ValueString() != "" {
			if current != priorIdentity.ValueString() {
				resp.RequiresReplace = append(resp.RequiresReplace, path.Root("conn"))
			} else {
				plan.ConnectionIdentity = priorIdentity
			}
		}
	}

	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func hashString(s string) string {
	sum := sha256Sum([]byte(s))
	return sum
}

// validateResourceModel checks all configuration-only invariants. When
// requireResolved is false, unknown values are skipped so plan-time validation
// does not fail early. A known conn override is always parsed strictly.
func validateResourceModel(ctx context.Context, m *sshResourceModel, defaults *providerDefaults, requireResolved bool) diag.Diagnostics {
	var diags diag.Diagnostics

	if requireResolved {
		if _, err := resolveConnConfig(defaultsProfile(defaults), m.Conn); err != nil {
			diags.AddError("Invalid connection profile", err.Error())
		}
	} else if err := checkConnSyntax(m.Conn); err != nil {
		diags.AddError("Invalid connection override", err.Error())
	}
	if err := validateOptionalExclusive(m.Owner, m.UID, "owner", "uid"); err != nil {
		diags.AddError("Invalid ownership", err.Error())
	}
	if err := validateOptionalExclusive(m.Group, m.GID, "group", "gid"); err != nil {
		diags.AddError("Invalid ownership", err.Error())
	}
	checkNonNegativeID(m.UID, "uid", "top-level", &diags)
	checkNonNegativeID(m.GID, "gid", "top-level", &diags)
	if requireResolved {
		checkResolvedString(m.Owner, "owner", "top-level", &diags)
		checkResolvedInt64(m.UID, "uid", "top-level", &diags)
		checkResolvedString(m.Group, "group", "top-level", &diags)
		checkResolvedInt64(m.GID, "gid", "top-level", &diags)
	}

	umask, err := parseUmask(m.Umask)
	if err != nil {
		diags.AddError("Invalid umask", err.Error())
	}

	dirs, d := decodeDirectories(ctx, m.Directories)
	diags.Append(d...)
	files, d := decodeFiles(ctx, m.Files)
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}

	seen := map[string]string{}
	claimPath := func(p types.String, where string) bool {
		if p.IsUnknown() {
			if requireResolved {
				diags.AddError("Unknown path", where+" path is not known.")
			}
			return false
		}
		if p.IsNull() || p.ValueString() == "" {
			diags.AddError("Missing path", where+" requires a non-empty path.")
			return false
		}
		if err := ValidateManagedPath(p.ValueString()); err != nil {
			diags.AddError("Invalid path", fmt.Sprintf("%s: %s", where, err))
			return false
		}
		if prev, ok := seen[p.ValueString()]; ok {
			diags.AddError("Duplicate path", fmt.Sprintf("path %q is declared by both %s and %s.", p.ValueString(), prev, where))
			return false
		}
		seen[p.ValueString()] = where
		return true
	}

	for i, dir := range dirs {
		where := fmt.Sprintf("directory[%d]", i)
		claimPath(dir.Path, where)
		if err := validateOptionalExclusive(dir.Owner, dir.UID, "owner", "uid"); err != nil {
			diags.AddError("Invalid ownership", where+": "+err.Error())
		}
		if err := validateOptionalExclusive(dir.Group, dir.GID, "group", "gid"); err != nil {
			diags.AddError("Invalid ownership", where+": "+err.Error())
		}
		checkNonNegativeID(dir.UID, "uid", where, &diags)
		checkNonNegativeID(dir.GID, "gid", where, &diags)
		if requireResolved {
			checkResolvedString(dir.Mode, "mode", where, &diags)
			checkResolvedString(dir.Owner, "owner", where, &diags)
			checkResolvedInt64(dir.UID, "uid", where, &diags)
			checkResolvedString(dir.Group, "group", where, &diags)
			checkResolvedInt64(dir.GID, "gid", where, &diags)
			checkResolvedBool(dir.AutoImport, "auto_import", where, &diags)
		}
		if !dir.Mode.IsNull() && !dir.Mode.IsUnknown() {
			if _, err := parseMode(dir.Mode.ValueString()); err != nil {
				diags.AddError("Invalid mode", where+": "+err.Error())
			}
		}
		if err := validatePrincipalTypes(dir.Owner, dir.Group); err != nil {
			diags.AddError("Invalid ownership", where+": "+err.Error())
		}
	}

	for i, file := range files {
		where := fmt.Sprintf("file[%d]", i)
		claimPath(file.Path, where)
		if err := validateOptionalExclusive(file.Owner, file.UID, "owner", "uid"); err != nil {
			diags.AddError("Invalid ownership", where+": "+err.Error())
		}
		if err := validateOptionalExclusive(file.Group, file.GID, "group", "gid"); err != nil {
			diags.AddError("Invalid ownership", where+": "+err.Error())
		}
		checkNonNegativeID(file.UID, "uid", where, &diags)
		checkNonNegativeID(file.GID, "gid", where, &diags)
		if requireResolved {
			checkResolvedString(file.Mode, "mode", where, &diags)
			checkResolvedString(file.Owner, "owner", where, &diags)
			checkResolvedInt64(file.UID, "uid", where, &diags)
			checkResolvedString(file.Group, "group", where, &diags)
			checkResolvedInt64(file.GID, "gid", where, &diags)
			checkResolvedBool(file.AutoImport, "auto_import", where, &diags)
			checkResolvedString(file.Content, "content", where, &diags)
			checkResolvedString(file.ContentWO, "content_wo", where, &diags)
		}
		if !file.Mode.IsNull() && !file.Mode.IsUnknown() {
			if _, err := parseMode(file.Mode.ValueString()); err != nil {
				diags.AddError("Invalid mode", where+": "+err.Error())
			}
		}
		if err := validatePrincipalTypes(file.Owner, file.Group); err != nil {
			diags.AddError("Invalid ownership", where+": "+err.Error())
		}
		contentKnown := !file.Content.IsNull() && !file.Content.IsUnknown()
		woKnown := !file.ContentWO.IsNull() && !file.ContentWO.IsUnknown()
		if requireResolved {
			if contentKnown && woKnown {
				diags.AddError("Conflicting file content", where+" must set exactly one of content and content_wo, not both.")
			} else if !contentKnown && !woKnown {
				diags.AddError("Missing file content", where+" must set exactly one of content and content_wo. Use an empty string for empty content.")
			}
		} else {
			contentAny := file.Content.IsUnknown() || contentKnown
			woAny := file.ContentWO.IsUnknown() || woKnown
			if contentKnown && woKnown {
				diags.AddError("Conflicting file content", where+" must set exactly one of content and content_wo, not both.")
			} else if !contentAny && !woAny {
				diags.AddError("Missing file content", where+" must set exactly one of content and content_wo. Use an empty string for empty content.")
			}
		}
	}

	if m.Umask.IsUnknown() && requireResolved {
		diags.AddError("Unknown umask", "`umask` is not known.")
	}
	_ = umask
	return diags
}

func validateOptionalExclusive(a types.String, b types.Int64, aName, bName string) error {
	if !a.IsNull() && !a.IsUnknown() && !b.IsNull() && !b.IsUnknown() {
		return fmt.Errorf("%s and %s are mutually exclusive", aName, bName)
	}
	return nil
}

func checkNonNegativeID(v types.Int64, name, where string, diags *diag.Diagnostics) {
	if !v.IsNull() && !v.IsUnknown() && v.ValueInt64() < 0 {
		diags.AddError("Invalid "+name, fmt.Sprintf("%s %s must be non-negative.", where, name))
	}
}

func checkResolvedString(v types.String, name, where string, diags *diag.Diagnostics) {
	if v.IsUnknown() {
		diags.AddError("Unknown "+name, fmt.Sprintf("%s %s is not known at apply time.", where, name))
	}
}

func checkResolvedInt64(v types.Int64, name, where string, diags *diag.Diagnostics) {
	if v.IsUnknown() {
		diags.AddError("Unknown "+name, fmt.Sprintf("%s %s is not known at apply time.", where, name))
	}
}

func checkResolvedBool(v types.Bool, name, where string, diags *diag.Diagnostics) {
	if v.IsUnknown() {
		diags.AddError("Unknown "+name, fmt.Sprintf("%s %s is not known at apply time.", where, name))
	}
}

func validatePrincipalTypes(owner, group types.String) error {
	for _, v := range []struct {
		name string
		val  types.String
	}{{"owner", owner}, {"group", group}} {
		if v.val.IsNull() || v.val.IsUnknown() {
			continue
		}
		if err := remote.ValidatePrincipalName(v.val.ValueString()); err != nil {
			return fmt.Errorf("%s: %w", v.name, err)
		}
	}
	return nil
}

func decodeDirectories(ctx context.Context, list types.List) ([]directoryModel, diag.Diagnostics) {
	var out []directoryModel
	var diags diag.Diagnostics
	if list.IsNull() || list.IsUnknown() {
		return out, diags
	}
	diags.Append(list.ElementsAs(ctx, &out, false)...)
	return out, diags
}

func decodeFiles(ctx context.Context, list types.List) ([]fileModel, diag.Diagnostics) {
	var out []fileModel
	var diags diag.Diagnostics
	if list.IsNull() || list.IsUnknown() {
		return out, diags
	}
	diags.Append(list.ElementsAs(ctx, &out, false)...)
	return out, diags
}

func parseUmask(v types.String) (uint32, error) {
	if v.IsNull() || v.IsUnknown() {
		return 022, nil
	}
	if v.ValueString() == "" {
		return 0, fmt.Errorf("umask must not be empty")
	}
	n, err := strconv.ParseUint(v.ValueString(), 8, 32)
	if err != nil || n > 0777 {
		return 0, fmt.Errorf("umask %q must be an octal value between 000 and 777", v.ValueString())
	}
	return uint32(n), nil
}

func parseMode(s string) (os.FileMode, error) {
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > 0777 {
		return 0, fmt.Errorf("mode %q must be an octal value between 0000 and 0777", s)
	}
	return os.FileMode(n), nil
}

func modeString(m os.FileMode) string {
	return fmt.Sprintf("%04o", m.Perm())
}

func sha256Sum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
