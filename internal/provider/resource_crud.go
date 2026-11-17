package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
	"github.com/geektr-cloud/terraform-provider-ssh/internal/remote"
)

type ownershipSpec struct {
	owner types.String
	uid   types.Int64
	group types.String
	gid   types.Int64
}

type desiredEntry struct {
	path       string
	kind       remote.Type
	mode       os.FileMode
	modeText   string
	autoImport bool
	ownership  ownershipSpec
	content    []byte
	woHash     string
}

func (r *sshResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg sshResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateResourceModel(ctx, &cfg, true)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Validate the output cap before any dial, delete, or write.
	limit, err := maxOutputSize()
	if err != nil {
		resp.Diagnostics.AddError("Invalid output limit", err.Error())
		return
	}

	umask, err := resolveUmask(cfg.Umask, plan.Umask)
	if err != nil {
		resp.Diagnostics.AddError("Invalid umask", err.Error())
		return
	}
	desired, diags := buildDesired(ctx, &cfg, &plan, umask, limit)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	connModel, connDiags := effectiveConnection(ctx, &cfg, &plan)
	resp.Diagnostics.Append(connDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	connClient, dialDiags := dialResource(ctx, connModel)
	resp.Diagnostics.Append(dialDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	defer func() { _ = connClient.Close() }()

	fs := &remote.FS{Client: connClient, Timeout: connClient.Timeout(), Umask: umask, AllowSudo: true}

	var createdDirs []directoryModel
	var createdFiles []fileModel
	fail := func(err error) {
		diags.AddError("SSH resource apply failed", err.Error())
		resp.Diagnostics.Append(diags...)
		state := buildStateModel(ctx, &cfg, &plan, connModel, desired, umask, pathSetFromDirs(createdDirs), pathSetFromFiles(createdFiles))
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}

	applyOrder := append([]desiredEntry(nil), desired...)
	sortDesired(applyOrder)
	for _, entry := range applyOrder {
		if err := upsertEntry(ctx, fs, entry, false); err != nil {
			fail(err)
			return
		}
		if entry.kind == remote.Directory {
			createdDirs = append(createdDirs, desiredDirectoryModel(entry))
		} else {
			createdFiles = append(createdFiles, desiredFileModel(entry))
		}
	}

	state := buildStateModel(ctx, &cfg, &plan, connModel, desired, umask, nil, nil)
	if errs := readManagedPaths(ctx, fs, &state); len(errs) > 0 {
		resp.Diagnostics.Append(errs...)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sshResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sshResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Conn.IsNull() {
		resp.State.RemoveResource(ctx)
		return
	}
	if _, err := maxOutputSize(); err != nil {
		resp.Diagnostics.AddError("Invalid output limit", err.Error())
		return
	}
	connModel, connDiags := connectionFromObject(ctx, state.Conn)
	resp.Diagnostics.Append(connDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	connClient, diags := dialResource(ctx, connModel)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	defer func() { _ = connClient.Close() }()

	umask, _ := parseUmask(state.Umask)
	fs := &remote.FS{Client: connClient, Timeout: connClient.Timeout(), Umask: umask, AllowSudo: true}
	if errs := readManagedPaths(ctx, fs, &state); len(errs) > 0 {
		resp.Diagnostics.Append(errs...)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sshResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, cfg, prior sshResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateResourceModel(ctx, &cfg, true)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Validate the output cap before any dial, delete, or write.
	limit, err := maxOutputSize()
	if err != nil {
		resp.Diagnostics.AddError("Invalid output limit", err.Error())
		return
	}

	umask, err := resolveUmask(cfg.Umask, plan.Umask)
	if err != nil {
		resp.Diagnostics.AddError("Invalid umask", err.Error())
		return
	}
	desired, diags := buildDesired(ctx, &cfg, &plan, umask, limit)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	connModel, connDiags := effectiveConnection(ctx, &cfg, &plan)
	resp.Diagnostics.Append(connDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	connClient, dialDiags := dialResource(ctx, connModel)
	resp.Diagnostics.Append(dialDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	defer func() { _ = connClient.Close() }()

	fs := &remote.FS{Client: connClient, Timeout: connClient.Timeout(), Umask: umask, AllowSudo: true}

	priorDirs, d := decodeDirectories(ctx, prior.Directories)
	resp.Diagnostics.Append(d...)
	priorFiles, d := decodeFiles(ctx, prior.Files)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	desiredByPath := map[string]desiredEntry{}
	for _, e := range desired {
		desiredByPath[e.path] = e
	}

	// Delete paths removed from configuration or whose managed type changed,
	// longest first so children are handled before parents.
	type removal struct {
		path string
		kind remote.Type
	}
	var removals []removal
	for _, d := range priorDirs {
		e, ok := desiredByPath[d.Path.ValueString()]
		if !ok || e.kind != remote.Directory {
			removals = append(removals, removal{path: d.Path.ValueString(), kind: remote.Directory})
		}
	}
	for _, f := range priorFiles {
		e, ok := desiredByPath[f.Path.ValueString()]
		if !ok || e.kind != remote.File {
			removals = append(removals, removal{path: f.Path.ValueString(), kind: remote.File})
		}
	}
	sort.SliceStable(removals, func(i, j int) bool {
		return len(removals[i].path) > len(removals[j].path)
	})

	fail := func(err error) {
		resp.Diagnostics.AddError("SSH resource update failed", err.Error())
		prior.Directories = listDirectories(priorDirs)
		prior.Files = listFiles(priorFiles)
		resp.Diagnostics.Append(resp.State.Set(ctx, &prior)...)
	}

	for _, rem := range removals {
		if err := removeManagedPath(ctx, fs, rem.path, rem.kind); err != nil {
			fail(err)
			return
		}
		priorDirs = removeDirectoryModel(priorDirs, rem.path)
		priorFiles = removeFileModel(priorFiles, rem.path)
	}

	priorByPath := map[string]bool{}
	for _, d := range priorDirs {
		priorByPath[d.Path.ValueString()] = true
	}
	for _, f := range priorFiles {
		priorByPath[f.Path.ValueString()] = true
	}

	applyOrder := append([]desiredEntry(nil), desired...)
	sortDesired(applyOrder)
	for _, entry := range applyOrder {
		existed := priorByPath[entry.path]
		if err := upsertEntry(ctx, fs, entry, existed); err != nil {
			fail(err)
			return
		}
		if entry.kind == remote.Directory {
			priorDirs = upsertDirectoryModel(priorDirs, desiredDirectoryModel(entry))
			priorFiles = removeFileModel(priorFiles, entry.path)
		} else {
			priorFiles = upsertFileModel(priorFiles, desiredFileModel(entry))
			priorDirs = removeDirectoryModel(priorDirs, entry.path)
		}
		priorByPath[entry.path] = true
	}

	state := buildStateModel(ctx, &cfg, &plan, connModel, desired, umask, nil, nil)
	if errs := readManagedPaths(ctx, fs, &state); len(errs) > 0 {
		resp.Diagnostics.Append(errs...)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sshResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sshResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Conn.IsNull() {
		return
	}
	connModel, connDiags := connectionFromObject(ctx, state.Conn)
	resp.Diagnostics.Append(connDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	connClient, diags := dialResource(ctx, connModel)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	defer func() { _ = connClient.Close() }()

	umask, _ := parseUmask(state.Umask)
	fs := &remote.FS{Client: connClient, Timeout: connClient.Timeout(), Umask: umask, AllowSudo: true}

	dirs, d := decodeDirectories(ctx, state.Directories)
	resp.Diagnostics.Append(d...)
	files, d := decodeFiles(ctx, state.Files)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	type target struct {
		path string
		kind remote.Type
	}
	var targets []target
	for _, dir := range dirs {
		if !dir.Path.IsNull() {
			targets = append(targets, target{path: dir.Path.ValueString(), kind: remote.Directory})
		}
	}
	for _, f := range files {
		if !f.Path.IsNull() {
			targets = append(targets, target{path: f.Path.ValueString(), kind: remote.File})
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		return len(targets[i].path) > len(targets[j].path)
	})
	for _, t := range targets {
		if err := removeManagedPath(ctx, fs, t.path, t.kind); err != nil {
			resp.Diagnostics.AddError("SSH resource destroy failed", err.Error())
			return
		}
	}
}

func dialResource(ctx context.Context, m *ConnectionModel) (*client.Client, diag.Diagnostics) {
	var diags diag.Diagnostics
	cfg, d := toClientConfig(ctx, m)
	diags.Append(d...)
	if diags.HasError() {
		return nil, diags
	}
	c, err := client.Dial(ctx, cfg)
	if err != nil {
		diags.AddError("SSH connection failed", err.Error())
		return nil, diags
	}
	return c, diags
}

func buildDesired(ctx context.Context, cfg, plan *sshResourceModel, umask uint32, limit int64) ([]desiredEntry, diag.Diagnostics) {
	var diags diag.Diagnostics
	cfgFiles, d := decodeFiles(ctx, cfg.Files)
	diags.Append(d...)
	cfgDirs, d := decodeDirectories(ctx, cfg.Directories)
	diags.Append(d...)
	if diags.HasError() {
		return nil, diags
	}

	var planFiles []fileModel
	if plan != nil {
		var pd diag.Diagnostics
		planFiles, pd = decodeFiles(ctx, plan.Files)
		diags.Append(pd...)
		if diags.HasError() {
			return nil, diags
		}
	}
	plannedByPath := map[string]fileModel{}
	for _, f := range planFiles {
		if !f.Path.IsNull() && !f.Path.IsUnknown() {
			plannedByPath[f.Path.ValueString()] = f
		}
	}

	entries := make([]desiredEntry, 0, len(cfgDirs)+len(cfgFiles))
	for _, dir := range cfgDirs {
		entry, err := desiredFromDirectory(dir, cfg, umask)
		if err != nil {
			diags.AddError("Invalid directory", fmt.Sprintf("%s: %s", dir.Path.ValueString(), err))
			continue
		}
		entries = append(entries, entry)
	}
	for i, file := range cfgFiles {
		planned := plannedFileFor(i, file, planFiles, plannedByPath)
		entry, err := desiredFromFile(file, planned, cfg, umask, limit)
		if err != nil {
			diags.AddError("Invalid file", fmt.Sprintf("%s: %s", file.Path.ValueString(), err))
			continue
		}
		entries = append(entries, entry)
	}
	return entries, diags
}

func plannedFileFor(index int, cfg fileModel, planFiles []fileModel, byPath map[string]fileModel) fileModel {
	if !cfg.Path.IsNull() && !cfg.Path.IsUnknown() {
		if planned, ok := byPath[cfg.Path.ValueString()]; ok {
			return planned
		}
	}
	if index < len(planFiles) {
		return planFiles[index]
	}
	return fileModel{}
}

func desiredFromDirectory(d directoryModel, cfg *sshResourceModel, umask uint32) (desiredEntry, error) {
	if d.Path.IsNull() || d.Path.IsUnknown() || d.Path.ValueString() == "" {
		return desiredEntry{}, errors.New("path is not known at apply time")
	}
	autoImport, err := resolvedAutoImport(d.AutoImport)
	if err != nil {
		return desiredEntry{}, err
	}
	mode := os.FileMode(0777 &^ umask)
	modeText := modeString(mode)
	if d.Mode.IsUnknown() {
		return desiredEntry{}, errors.New("mode is not known at apply time")
	}
	if !d.Mode.IsNull() {
		parsed, err := parseMode(d.Mode.ValueString())
		if err != nil {
			return desiredEntry{}, err
		}
		mode = parsed
		modeText = d.Mode.ValueString()
	}
	ownership, err := resolveOwnership(ownershipFieldsFromDirectory(d), cfg)
	if err != nil {
		return desiredEntry{}, err
	}
	return desiredEntry{
		path:       d.Path.ValueString(),
		kind:       remote.Directory,
		mode:       mode,
		modeText:   modeText,
		autoImport: autoImport,
		ownership:  ownership,
	}, nil
}

func desiredFromFile(f fileModel, planned fileModel, cfg *sshResourceModel, umask uint32, limit int64) (desiredEntry, error) {
	if f.Path.IsNull() || f.Path.IsUnknown() || f.Path.ValueString() == "" {
		return desiredEntry{}, errors.New("path is not known at apply time")
	}
	autoImport, err := resolvedAutoImport(f.AutoImport)
	if err != nil {
		return desiredEntry{}, err
	}
	mode := os.FileMode(0666 &^ umask)
	modeText := modeString(mode)
	if f.Mode.IsUnknown() {
		return desiredEntry{}, errors.New("mode is not known at apply time")
	}
	if !f.Mode.IsNull() {
		parsed, err := parseMode(f.Mode.ValueString())
		if err != nil {
			return desiredEntry{}, err
		}
		mode = parsed
		modeText = f.Mode.ValueString()
	}
	ownership, err := resolveOwnership(ownershipFieldsFromFile(f), cfg)
	if err != nil {
		return desiredEntry{}, err
	}
	if f.Content.IsUnknown() || f.ContentWO.IsUnknown() {
		return desiredEntry{}, errors.New("file content is not known at apply time")
	}
	contentSet := !f.Content.IsNull()
	woSet := !f.ContentWO.IsNull()
	if contentSet && woSet {
		return desiredEntry{}, fmt.Errorf("file %q must set exactly one of content and content_wo", f.Path.ValueString())
	}
	if !contentSet && !woSet {
		return desiredEntry{}, fmt.Errorf("file %q must set exactly one of content and content_wo", f.Path.ValueString())
	}

	entry := desiredEntry{
		path:       f.Path.ValueString(),
		kind:       remote.File,
		mode:       mode,
		modeText:   modeText,
		autoImport: autoImport,
		ownership:  ownership,
	}
	if woSet {
		entry.content = []byte(f.ContentWO.ValueString())
		entry.woHash = sha256Sum(entry.content)
		plannedHash := planned.ContentSHA256
		if !plannedHash.IsNull() && !plannedHash.IsUnknown() && plannedHash.ValueString() != "" &&
			!strings.EqualFold(plannedHash.ValueString(), entry.woHash) {
			return desiredEntry{}, fmt.Errorf(
				"file %q content_wo changed between plan and apply; refusing to write unstable write-only content",
				f.Path.ValueString(),
			)
		}
	} else {
		entry.content = []byte(f.Content.ValueString())
		if int64(len(entry.content)) > limit {
			return desiredEntry{}, fmt.Errorf(
				"file %q content is %d bytes, exceeding the configured limit of %d bytes",
				f.Path.ValueString(), len(entry.content), limit,
			)
		}
	}
	return entry, nil
}

func resolvedAutoImport(v types.Bool) (bool, error) {
	if v.IsUnknown() {
		return false, errors.New("auto_import is not known at apply time")
	}
	if v.IsNull() {
		return false, nil
	}
	return v.ValueBool(), nil
}

type ownershipFields struct {
	owner types.String
	uid   types.Int64
	group types.String
	gid   types.Int64
}

func ownershipFieldsFromDirectory(d directoryModel) ownershipFields {
	return ownershipFields{owner: d.Owner, uid: d.UID, group: d.Group, gid: d.GID}
}

func ownershipFieldsFromFile(f fileModel) ownershipFields {
	return ownershipFields{owner: f.Owner, uid: f.UID, group: f.Group, gid: f.GID}
}

// resolveOwnership uses configuration values. Omitted (null) block fields
// inherit the top-level defaults; unknown block fields are unresolved and must
// not silently fall back. A block that explicitly sets either owner or uid
// overrides both top-level fields; the same applies to group/gid.
func resolveOwnership(block ownershipFields, cfg *sshResourceModel) (ownershipSpec, error) {
	if block.owner.IsUnknown() || block.uid.IsUnknown() || block.group.IsUnknown() || block.gid.IsUnknown() {
		return ownershipSpec{}, errors.New("ownership is not known at apply time")
	}
	out := ownershipSpec{}
	if block.owner.IsNull() && block.uid.IsNull() {
		out.owner = cfg.Owner
		out.uid = cfg.UID
	} else {
		out.owner = block.owner
		out.uid = block.uid
	}
	if block.group.IsNull() && block.gid.IsNull() {
		out.group = cfg.Group
		out.gid = cfg.GID
	} else {
		out.group = block.group
		out.gid = block.gid
	}
	if out.owner.IsUnknown() || out.uid.IsUnknown() || out.group.IsUnknown() || out.gid.IsUnknown() {
		return ownershipSpec{}, errors.New("ownership is not known at apply time")
	}
	return out, nil
}

func resolveUmask(cfg, plan types.String) (uint32, error) {
	if cfg.IsUnknown() {
		return 0, errors.New("umask is not known at apply time")
	}
	if !cfg.IsNull() {
		return parseUmask(cfg)
	}
	if !plan.IsNull() && !plan.IsUnknown() {
		return parseUmask(plan)
	}
	return 022, nil
}

func resolvedUmaskValue(cfg, plan types.String, parsed uint32) types.String {
	if !cfg.IsNull() && !cfg.IsUnknown() && cfg.ValueString() != "" {
		return cfg
	}
	if !plan.IsNull() && !plan.IsUnknown() && plan.ValueString() != "" {
		return plan
	}
	return types.StringValue(fmt.Sprintf("%03o", parsed))
}

func buildStateModel(ctx context.Context, cfg, plan *sshResourceModel, connModel *ConnectionModel, desired []desiredEntry, umask uint32, allowedDirs, allowedFiles map[string]bool) sshResourceModel {
	state := *cfg
	state.Umask = resolvedUmaskValue(cfg.Umask, plan.Umask, umask)
	if connModel != nil {
		if obj, d := connObjectValue(ctx, normalizedConnectionModel(connModel)); !d.HasError() {
			state.Conn = obj
		}
	}
	state.Directories = desiredDirectories(desired, allowedDirs)
	state.Files = desiredFiles(desired, allowedFiles)
	return state
}

func effectiveConnection(ctx context.Context, cfg, plan *sshResourceModel) (*ConnectionModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	cfgModel, d := connectionFromObject(ctx, cfg.Conn)
	diags.Append(d...)
	if diags.HasError() {
		return nil, diags
	}
	var planModel *ConnectionModel
	if plan != nil {
		if pm, pd := connectionFromObject(ctx, plan.Conn); !pd.HasError() {
			planModel = pm
		}
	}
	return mergeConnectionModel(cfgModel, planModel), diags
}

// mergeConnectionModel uses configuration values and only fills
// Optional+Computed connection fields from the plan when the configuration
// omits them.
func mergeConnectionModel(cfg, plan *ConnectionModel) *ConnectionModel {
	out := *cfg
	if plan == nil {
		return &out
	}
	out.Type = preferKnownString(cfg.Type, plan.Type)
	out.Port = preferKnownString(cfg.Port, plan.Port)
	out.InsecureIgnoreHostKey = preferKnownBool(cfg.InsecureIgnoreHostKey, plan.InsecureIgnoreHostKey)
	out.Envs = preferKnownMap(cfg.Envs, plan.Envs)
	out.Timeout = preferKnownString(cfg.Timeout, plan.Timeout)
	out.RetryDelay = preferKnownString(cfg.RetryDelay, plan.RetryDelay)
	if cfg.Jump != nil {
		j := *cfg.Jump
		if plan.Jump != nil {
			j.Port = preferKnownString(j.Port, plan.Jump.Port)
			j.InsecureIgnoreHostKey = preferKnownBool(j.InsecureIgnoreHostKey, plan.Jump.InsecureIgnoreHostKey)
		}
		out.Jump = &j
	}
	return &out
}

func preferKnownString(cfg, plan types.String) types.String {
	if !cfg.IsNull() && !cfg.IsUnknown() {
		return cfg
	}
	if !plan.IsNull() && !plan.IsUnknown() {
		return plan
	}
	return cfg
}

func preferKnownBool(cfg, plan types.Bool) types.Bool {
	if !cfg.IsNull() && !cfg.IsUnknown() {
		return cfg
	}
	if !plan.IsNull() && !plan.IsUnknown() {
		return plan
	}
	return cfg
}

func preferKnownMap(cfg, plan types.Map) types.Map {
	if !cfg.IsNull() && !cfg.IsUnknown() {
		return cfg
	}
	if !plan.IsNull() && !plan.IsUnknown() {
		return plan
	}
	return cfg
}

func sortDesired(entries []desiredEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return len(entries[i].path) < len(entries[j].path)
	})
}

// desiredDirectories renders desired entries in configuration order. allowed,
// when non-nil, restricts the result to the listed paths.
func desiredDirectories(desired []desiredEntry, allowed map[string]bool) types.List {
	out := make([]directoryModel, 0, len(desired))
	for _, e := range desired {
		if e.kind != remote.Directory {
			continue
		}
		if allowed != nil && !allowed[e.path] {
			continue
		}
		out = append(out, desiredDirectoryModel(e))
	}
	return listDirectories(out)
}

func desiredFiles(desired []desiredEntry, allowed map[string]bool) types.List {
	out := make([]fileModel, 0, len(desired))
	for _, e := range desired {
		if e.kind != remote.File {
			continue
		}
		if allowed != nil && !allowed[e.path] {
			continue
		}
		out = append(out, desiredFileModel(e))
	}
	return listFiles(out)
}

func pathSetFromDirs(dirs []directoryModel) map[string]bool {
	out := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		out[d.Path.ValueString()] = true
	}
	return out
}

func pathSetFromFiles(files []fileModel) map[string]bool {
	out := make(map[string]bool, len(files))
	for _, f := range files {
		out[f.Path.ValueString()] = true
	}
	return out
}

func upsertEntry(ctx context.Context, fs *remote.FS, entry desiredEntry, priorExists bool) error {
	// Resolve ownership names before touching the target. A bad or unknown
	// owner/group must never overwrite existing content or create a new target.
	uid, gid, err := resolveOwnershipIDs(ctx, fs, entry.ownership)
	if err != nil {
		return err
	}

	if err := fs.EnsureParents(ctx, entry.path); err != nil {
		return err
	}
	st, err := fs.Lstat(ctx, entry.path)
	exists := true
	if err != nil {
		if !client.IsNotExistError(err) {
			return fmt.Errorf("inspect %q: %w", entry.path, err)
		}
		exists = false
	}

	if exists {
		switch st.Type {
		case remote.Symlink:
			return fmt.Errorf("path %q is a symbolic link; managed paths must not be symlinks", entry.path)
		case remote.Other:
			return fmt.Errorf("path %q is a special file and cannot be managed", entry.path)
		}
		if st.Type != entry.kind {
			return fmt.Errorf("path %q already exists as a %s, not a %s", entry.path, st.Type, entry.kind)
		}
		if !priorExists && !entry.autoImport {
			return fmt.Errorf("path %q already exists; set auto_import = true to adopt it", entry.path)
		}
	}

	switch entry.kind {
	case remote.Directory:
		created := false
		if !exists {
			if err := fs.Mkdir(ctx, entry.path, entry.mode); err != nil {
				if errors.Is(err, remote.ErrDirectoryMayExist) {
					cleanupCreatedDirectory(fs, entry.path, true)
				}
				return fmt.Errorf("create directory %q: %w", entry.path, err)
			}
			created = true
		}
		if err := fs.Chmod(ctx, entry.path, entry.mode); err != nil {
			cleanupCreatedDirectory(fs, entry.path, created)
			return fmt.Errorf("set mode on %q: %w", entry.path, err)
		}
		if err := applyOwnershipIDs(ctx, fs, entry.path, uid, gid); err != nil {
			cleanupCreatedDirectory(fs, entry.path, created)
			return err
		}
	case remote.File:
		// WriteFileAtomic applies mode and ownership to the temporary file and
		// renames only after they succeed, so a fully written target is always
		// safe to record in state.
		if err := fs.WriteFileAtomic(ctx, entry.path, entry.content, entry.mode, uid, gid); err != nil {
			return fmt.Errorf("write file %q: %w", entry.path, err)
		}
	}
	return nil
}

func cleanupCreatedDirectory(fs *remote.FS, p string, created bool) {
	if !created {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = fs.RemoveDirectory(ctx, p)
}

func resolveOwnershipIDs(ctx context.Context, fs *remote.FS, spec ownershipSpec) (int, int, error) {
	if spec.owner.IsUnknown() || spec.uid.IsUnknown() || spec.group.IsUnknown() || spec.gid.IsUnknown() {
		return 0, 0, errors.New("ownership is not known at apply time")
	}
	uid := -1
	gid := -1
	if !spec.uid.IsNull() {
		uid = int(spec.uid.ValueInt64())
	} else if !spec.owner.IsNull() {
		resolved, err := fs.UIDForName(ctx, spec.owner.ValueString())
		if err != nil {
			return 0, 0, err
		}
		uid = resolved
	}
	if !spec.gid.IsNull() {
		gid = int(spec.gid.ValueInt64())
	} else if !spec.group.IsNull() {
		resolved, err := fs.GIDForName(ctx, spec.group.ValueString())
		if err != nil {
			return 0, 0, err
		}
		gid = resolved
	}
	return uid, gid, nil
}

func applyOwnershipIDs(ctx context.Context, fs *remote.FS, p string, uid, gid int) error {
	if uid < 0 && gid < 0 {
		return nil
	}
	if err := fs.Chown(ctx, p, uid, gid); err != nil {
		return fmt.Errorf("set ownership on %q: %w", p, err)
	}
	return nil
}

func removeManagedPath(ctx context.Context, fs *remote.FS, p string, expected remote.Type) error {
	st, err := fs.Lstat(ctx, p)
	if err != nil {
		if client.IsNotExistError(err) {
			return nil
		}
		return fmt.Errorf("inspect %q: %w", p, err)
	}
	if st.Type != expected {
		return fmt.Errorf("path %q is now a %s, but state expects a %s; refusing to remove it", p, st.Type, expected)
	}
	if st.Type == remote.Directory {
		if err := fs.RemoveDirectory(ctx, p); err != nil {
			if client.IsNotExistError(err) {
				return nil
			}
			return fmt.Errorf("remove directory %q (it must be empty; the provider never removes recursively): %w", p, err)
		}
		return nil
	}
	if err := fs.RemoveFile(ctx, p); err != nil && !client.IsNotExistError(err) {
		return fmt.Errorf("remove file %q: %w", p, err)
	}
	return nil
}

func readManagedPaths(ctx context.Context, fs *remote.FS, state *sshResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	limit, err := maxOutputSize()
	if err != nil {
		diags.AddError("Invalid output limit", err.Error())
		return diags
	}
	dirs, d := decodeDirectories(ctx, state.Directories)
	diags.Append(d...)
	files, d := decodeFiles(ctx, state.Files)
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}

	keptDirs := make([]directoryModel, 0, len(dirs))
	for _, dir := range dirs {
		p := dir.Path.ValueString()
		st, err := fs.Lstat(ctx, p)
		if err != nil {
			if client.IsNotExistError(err) {
				continue
			}
			diags.AddError("Read managed directory failed", fmt.Sprintf("%s: %s", p, err))
			keptDirs = append(keptDirs, dir)
			continue
		}
		if st.Type == remote.Symlink {
			diags.AddError("Managed path became a symlink", fmt.Sprintf("%s is now a symbolic link; managed paths must not be symlinks.", p))
			keptDirs = append(keptDirs, dir)
			continue
		}
		if st.Type != remote.Directory {
			diags.AddError("Managed path type changed", fmt.Sprintf("%s is now a %s, not a directory.", p, st.Type))
			keptDirs = append(keptDirs, dir)
			continue
		}
		fillMetadata(ctx, fs, &dir.Mode, &dir.Owner, &dir.UID, &dir.Group, &dir.GID, st)
		keptDirs = append(keptDirs, dir)
	}

	keptFiles := make([]fileModel, 0, len(files))
	for _, file := range files {
		p := file.Path.ValueString()
		st, err := fs.Lstat(ctx, p)
		if err != nil {
			if client.IsNotExistError(err) {
				continue
			}
			diags.AddError("Read managed file failed", fmt.Sprintf("%s: %s", p, err))
			keptFiles = append(keptFiles, file)
			continue
		}
		if st.Type == remote.Symlink {
			diags.AddError("Managed path became a symlink", fmt.Sprintf("%s is now a symbolic link; managed paths must not be symlinks.", p))
			keptFiles = append(keptFiles, file)
			continue
		}
		if st.Type != remote.File {
			diags.AddError("Managed path type changed", fmt.Sprintf("%s is now a %s, not a regular file.", p, st.Type))
			keptFiles = append(keptFiles, file)
			continue
		}
		if file.Content.IsNull() || file.Content.IsUnknown() {
			digest, err := fs.HashFile(ctx, p)
			if err != nil {
				diags.AddError("Read managed file failed", fmt.Sprintf("%s: %s", p, err))
				keptFiles = append(keptFiles, file)
				continue
			}
			file.ContentSHA256 = types.StringValue(digest)
			file.Content = types.StringNull()
			file.ContentWO = types.StringNull()
		} else {
			content, err := fs.ReadFile(ctx, p, limit)
			if err != nil {
				diags.AddError("Read managed file failed", fmt.Sprintf("%s: %s", p, err))
				keptFiles = append(keptFiles, file)
				continue
			}
			file.Content = types.StringValue(string(content))
			file.ContentSHA256 = types.StringNull()
			file.ContentWO = types.StringNull()
		}
		fillMetadata(ctx, fs, &file.Mode, &file.Owner, &file.UID, &file.Group, &file.GID, st)
		keptFiles = append(keptFiles, file)
	}

	state.Directories = listDirectories(keptDirs)
	state.Files = listFiles(keptFiles)
	return diags
}

func fillMetadata(ctx context.Context, fs *remote.FS, mode *types.String, owner *types.String, uid *types.Int64, group *types.String, gid *types.Int64, st *remote.Stat) {
	if !mode.IsNull() && !mode.IsUnknown() {
		parsed, err := parseMode(mode.ValueString())
		if err != nil || parsed.Perm() != st.Mode.Perm() {
			// Preserve the configured spelling (for example "644" instead of
			// "0644") only while the remote mode still matches it.
			*mode = types.StringValue(modeString(st.Mode.Perm()))
		}
	} else {
		*mode = types.StringValue(modeString(st.Mode.Perm()))
	}
	if owner.IsNull() || owner.IsUnknown() {
		if name := fs.NameForUID(ctx, st.UID); name != "" {
			*owner = types.StringValue(name)
		} else {
			*owner = types.StringNull()
		}
	}
	*uid = types.Int64Value(int64(st.UID))
	if group.IsNull() || group.IsUnknown() {
		if name := fs.NameForGID(ctx, st.GID); name != "" {
			*group = types.StringValue(name)
		} else {
			*group = types.StringNull()
		}
	}
	*gid = types.Int64Value(int64(st.GID))
}

func desiredDirectoryModel(e desiredEntry) directoryModel {
	return directoryModel{
		Path:       types.StringValue(e.path),
		Mode:       types.StringValue(e.modeText),
		Owner:      e.ownership.owner,
		UID:        e.ownership.uid,
		Group:      e.ownership.group,
		GID:        e.ownership.gid,
		AutoImport: types.BoolValue(e.autoImport),
	}
}

func desiredFileModel(e desiredEntry) fileModel {
	m := fileModel{
		Path:       types.StringValue(e.path),
		Mode:       types.StringValue(e.modeText),
		Owner:      e.ownership.owner,
		UID:        e.ownership.uid,
		Group:      e.ownership.group,
		GID:        e.ownership.gid,
		AutoImport: types.BoolValue(e.autoImport),
		ContentWO:  types.StringNull(),
	}
	if e.woHash != "" {
		m.Content = types.StringNull()
		m.ContentSHA256 = types.StringValue(e.woHash)
	} else {
		m.Content = types.StringValue(string(e.content))
		m.ContentSHA256 = types.StringNull()
	}
	return m
}

func listDirectories(dirs []directoryModel) types.List {
	if dirs == nil {
		dirs = []directoryModel{}
	}
	v, d := types.ListValueFrom(context.Background(), types.ObjectType{AttrTypes: directoryAttrTypes()}, dirs)
	if d.HasError() {
		return types.ListValueMust(types.ObjectType{AttrTypes: directoryAttrTypes()}, []attr.Value{})
	}
	return v
}

func listFiles(files []fileModel) types.List {
	if files == nil {
		files = []fileModel{}
	}
	v, d := types.ListValueFrom(context.Background(), types.ObjectType{AttrTypes: fileAttrTypes()}, files)
	if d.HasError() {
		return types.ListValueMust(types.ObjectType{AttrTypes: fileAttrTypes()}, []attr.Value{})
	}
	return v
}

func upsertDirectoryModel(list []directoryModel, m directoryModel) []directoryModel {
	for i := range list {
		if list[i].Path.ValueString() == m.Path.ValueString() {
			list[i] = m
			return list
		}
	}
	return append(list, m)
}

func upsertFileModel(list []fileModel, m fileModel) []fileModel {
	for i := range list {
		if list[i].Path.ValueString() == m.Path.ValueString() {
			list[i] = m
			return list
		}
	}
	return append(list, m)
}

func removeDirectoryModel(list []directoryModel, p string) []directoryModel {
	out := list[:0]
	for _, m := range list {
		if m.Path.ValueString() != p {
			out = append(out, m)
		}
	}
	return out
}

func removeFileModel(list []fileModel, p string) []fileModel {
	out := list[:0]
	for _, m := range list {
		if m.Path.ValueString() != p {
			out = append(out, m)
		}
	}
	return out
}
