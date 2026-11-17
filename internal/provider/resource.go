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
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/remote"
)

var _ resource.Resource = (*sshResource)(nil)
var _ resource.ResourceWithModifyPlan = (*sshResource)(nil)

type sshResource struct{}

// sshResourceModel is the full resource state.
type sshResourceModel struct {
	Conn        types.Object `tfsdk:"conn"`
	Umask       types.String `tfsdk:"umask"`
	Owner       types.String `tfsdk:"owner"`
	UID         types.Int64  `tfsdk:"uid"`
	Group       types.String `tfsdk:"group"`
	GID         types.Int64  `tfsdk:"gid"`
	Directories types.List   `tfsdk:"directory"`
	Files       types.List   `tfsdk:"file"`
}

func connectionFromObject(ctx context.Context, obj types.Object) (*ConnectionModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	if obj.IsNull() {
		diags.AddError("Missing connection profile", "`conn` is required.")
		return nil, diags
	}
	if obj.IsUnknown() {
		diags.AddError("Unknown connection profile", "`conn` is not known during this operation.")
		return nil, diags
	}
	var m ConnectionModel
	diags.Append(obj.As(ctx, &m, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, diags
	}
	return &m, diags
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
			"conn": schema.SingleNestedAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "Connection Profile. Persisted and reconnected for every lifecycle operation.",
				Attributes:  connInputAttributes(),
				PlanModifiers: []planmodifier.Object{
					connIdentityPlanModifier{},
				},
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

// connIdentityPlanModifier replaces the resource when the connection identity
// (target or jump host/port/user) changes. Credential-only changes keep the
// remote paths.
type connIdentityPlanModifier struct{}

func (m connIdentityPlanModifier) Description(_ context.Context) string {
	return "Replace the resource when the target or jump identity changes."
}

func (m connIdentityPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m connIdentityPlanModifier) PlanModifyObject(ctx context.Context, req planmodifier.ObjectRequest, resp *planmodifier.ObjectResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || req.PlanValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}
	if connectionIdentityUnknown(req.StateValue) || connectionIdentityUnknown(req.PlanValue) {
		// An unknown identity cannot be compared. Do not plan a replacement
		// based on a zero-value comparison; the apply will resolve it.
		return
	}
	var before, after ConnectionModel
	resp.Diagnostics.Append(req.StateValue.As(ctx, &before, basetypes.ObjectAsOptions{})...)
	resp.Diagnostics.Append(req.PlanValue.As(ctx, &after, basetypes.ObjectAsOptions{})...)
	if resp.Diagnostics.HasError() {
		return
	}
	if connectionIdentityChanged(&before, &after) {
		resp.RequiresReplace = true
	}
}

// connectionIdentityUnknown reports whether any identity field used for
// replacement is unknown. Jump is optional; an unknown jump object is unknown.
func connectionIdentityUnknown(obj types.Object) bool {
	attrs := obj.Attributes()
	for _, name := range []string{"host", "port", "user"} {
		if v, ok := attrs[name]; ok && v.IsUnknown() {
			return true
		}
	}
	jump, ok := attrs["jump"]
	if !ok || jump.IsNull() {
		return false
	}
	if jump.IsUnknown() {
		return true
	}
	jumpObj, ok := jump.(types.Object)
	if !ok {
		return true
	}
	for _, name := range []string{"host", "port", "user"} {
		if v, ok := jumpObj.Attributes()[name]; ok && v.IsUnknown() {
			return true
		}
	}
	return false
}

func connectionIdentityChanged(a, b *ConnectionModel) bool {
	if identityStringUnknown(a.Host) || identityStringUnknown(a.Port) || identityStringUnknown(a.User) ||
		identityStringUnknown(b.Host) || identityStringUnknown(b.Port) || identityStringUnknown(b.User) {
		return false
	}
	if a.Host.ValueString() != b.Host.ValueString() ||
		a.Port.ValueString() != b.Port.ValueString() ||
		a.User.ValueString() != b.User.ValueString() {
		return true
	}
	aJump, bJump := a.Jump, b.Jump
	if (aJump == nil) != (bJump == nil) {
		return true
	}
	if aJump != nil {
		if identityStringUnknown(aJump.Host) || identityStringUnknown(aJump.Port) || identityStringUnknown(aJump.User) ||
			identityStringUnknown(bJump.Host) || identityStringUnknown(bJump.Port) || identityStringUnknown(bJump.User) {
			return false
		}
		if aJump.Host.ValueString() != bJump.Host.ValueString() ||
			aJump.Port.ValueString() != bJump.Port.ValueString() ||
			aJump.User.ValueString() != bJump.User.ValueString() {
			return true
		}
	}
	return false
}

func identityStringUnknown(v types.String) bool { return v.IsUnknown() }

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

	resp.Diagnostics.Append(validateResourceModel(ctx, &cfgModel, false)...)
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
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func hashString(s string) string {
	sum := sha256Sum([]byte(s))
	return sum
}

// validateResourceModel checks all configuration-only invariants. When
// requireResolved is false, unknown values are skipped so plan-time validation
// does not fail early.
func validateResourceModel(ctx context.Context, m *sshResourceModel, requireResolved bool) diag.Diagnostics {
	var diags diag.Diagnostics

	if m.Conn.IsNull() {
		diags.AddError("Missing connection profile", "`conn` is required.")
		return diags
	}
	if requireResolved {
		connModel, d := connectionFromObject(ctx, m.Conn)
		diags.Append(d...)
		if !diags.HasError() {
			if _, d := toClientConfig(ctx, connModel); d.HasError() {
				diags.Append(d...)
			}
		}
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
