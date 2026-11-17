package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
	"github.com/geektr-cloud/terraform-provider-ssh/internal/remote"
)

const outputLimitEnv = "SSH_PROVIDER_MAX_OUTPUT_SIZE"

// OpsModel is the shared configuration for data.ssh_data,
// data.ssh_sensitive_data, and ephemeral.ssh_data.
type OpsModel struct {
	Conn     *ConnectionModel `tfsdk:"conn"`
	Sudo     types.Bool       `tfsdk:"sudo"`
	Timeout  types.String     `tfsdk:"timeout"`
	Files    types.List       `tfsdk:"file"`
	Commands types.List       `tfsdk:"command"`
	Runs     types.List       `tfsdk:"run"`
	Values   types.Map        `tfsdk:"values"`
}

// FileOperationModel is one file read operation.
type FileOperationModel struct {
	Key     types.String `tfsdk:"key"`
	Path    types.String `tfsdk:"path"`
	Index   types.Int64  `tfsdk:"index"`
	Trim    types.Bool   `tfsdk:"trim"`
	Sudo    types.Bool   `tfsdk:"sudo"`
	Timeout types.String `tfsdk:"timeout"`
}

// CommandOperationModel is one argv command operation.
type CommandOperationModel struct {
	Key     types.String `tfsdk:"key"`
	Command types.List   `tfsdk:"command"`
	Index   types.Int64  `tfsdk:"index"`
	Trim    types.Bool   `tfsdk:"trim"`
	Sudo    types.Bool   `tfsdk:"sudo"`
	Timeout types.String `tfsdk:"timeout"`
}

// RunOperationModel is one remote script operation.
type RunOperationModel struct {
	Key     types.String `tfsdk:"key"`
	Script  types.String `tfsdk:"script"`
	Shell   types.String `tfsdk:"shell"`
	Envs    types.Map    `tfsdk:"envs"`
	Index   types.Int64  `tfsdk:"index"`
	Trim    types.Bool   `tfsdk:"trim"`
	Sudo    types.Bool   `tfsdk:"sudo"`
	Timeout types.String `tfsdk:"timeout"`
}

func fileOperationAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"key":     types.StringType,
		"path":    types.StringType,
		"index":   types.Int64Type,
		"trim":    types.BoolType,
		"sudo":    types.BoolType,
		"timeout": types.StringType,
	}
}

func commandOperationAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"key":     types.StringType,
		"command": types.ListType{ElemType: types.StringType},
		"index":   types.Int64Type,
		"trim":    types.BoolType,
		"sudo":    types.BoolType,
		"timeout": types.StringType,
	}
}

func runOperationAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"key":     types.StringType,
		"script":  types.StringType,
		"shell":   types.StringType,
		"envs":    types.MapType{ElemType: types.StringType},
		"index":   types.Int64Type,
		"trim":    types.BoolType,
		"sudo":    types.BoolType,
		"timeout": types.StringType,
	}
}

func (m *OpsModel) decodeOperations(ctx context.Context) ([]FileOperationModel, []CommandOperationModel, []RunOperationModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	var files []FileOperationModel
	var commands []CommandOperationModel
	var runs []RunOperationModel
	if !m.Files.IsNull() && !m.Files.IsUnknown() {
		diags.Append(m.Files.ElementsAs(ctx, &files, false)...)
	}
	if !m.Commands.IsNull() && !m.Commands.IsUnknown() {
		diags.Append(m.Commands.ElementsAs(ctx, &commands, false)...)
	}
	if !m.Runs.IsNull() && !m.Runs.IsUnknown() {
		diags.Append(m.Runs.ElementsAs(ctx, &runs, false)...)
	}
	return files, commands, runs, diags
}

type operationKind int

const (
	fileOperation operationKind = iota
	commandOperation
	runOperation
)

type plannedOperation struct {
	index int64
	kind  operationKind
	order int
	file  *FileOperationModel
	cmd   *CommandOperationModel
	run   *RunOperationModel
}

// Validate performs all configuration checks before any remote operation.
func (m *OpsModel) Validate(ctx context.Context) (client.Config, int64, diag.Diagnostics) {
	var diags diag.Diagnostics
	cfg, connDiags := toClientConfig(ctx, m.Conn)
	diags.Append(connDiags...)
	if diags.HasError() {
		return client.Config{}, 0, diags
	}

	limit, err := maxOutputSize()
	if err != nil {
		diags.AddError("Invalid output limit", err.Error())
		return client.Config{}, 0, diags
	}

	if m.Sudo.IsUnknown() {
		diags.AddError("Unknown sudo value", "`sudo` is not known during this operation.")
		return client.Config{}, 0, diags
	}
	if _, err := durationFrom(m.Timeout, "5m", "timeout"); err != nil {
		diags.AddError("Invalid timeout", err.Error())
		return client.Config{}, 0, diags
	}

	files, commands, runs, d := m.decodeOperations(ctx)
	diags.Append(d...)
	if diags.HasError() {
		return client.Config{}, 0, diags
	}

	seen := map[string]string{}
	claimKey := func(key types.String, where string) bool {
		if key.IsUnknown() {
			diags.AddError("Unknown operation key", fmt.Sprintf("%s key is not known during this operation.", where))
			return false
		}
		if key.IsNull() || key.ValueString() == "" {
			diags.AddError("Missing operation key", fmt.Sprintf("%s requires a non-empty `key`.", where))
			return false
		}
		if prev, ok := seen[key.ValueString()]; ok {
			diags.AddError("Duplicate operation key", fmt.Sprintf("key %q is used by both %s and %s.", key.ValueString(), prev, where))
			return false
		}
		seen[key.ValueString()] = where
		return true
	}
	checkCommon := func(index types.Int64, trim, sudo types.Bool, timeout types.String, where string) {
		if index.IsUnknown() {
			diags.AddError("Unknown index", fmt.Sprintf("%s index is not known during this operation.", where))
		} else if !index.IsNull() && index.ValueInt64() < 0 {
			diags.AddError("Invalid index", fmt.Sprintf("%s index must be non-negative.", where))
		}
		if trim.IsUnknown() {
			diags.AddError("Unknown trim flag", fmt.Sprintf("%s trim is not known during this operation.", where))
		}
		if sudo.IsUnknown() {
			diags.AddError("Unknown sudo flag", fmt.Sprintf("%s sudo is not known during this operation.", where))
		}
		if _, err := durationFrom(timeout, "", where+" timeout"); err != nil {
			diags.AddError("Invalid timeout", err.Error())
		}
	}

	for i := range files {
		f := &files[i]
		where := fmt.Sprintf("file[%d]", i)
		if !claimKey(f.Key, where) {
			continue
		}
		if f.Path.IsUnknown() {
			diags.AddError("Unknown file path", fmt.Sprintf("%s path is not known during this operation.", where))
		} else if f.Path.IsNull() || f.Path.ValueString() == "" {
			diags.AddError("Missing file path", fmt.Sprintf("%s requires a non-empty `path`.", where))
		} else if err := ValidateManagedPath(f.Path.ValueString()); err != nil {
			diags.AddError("Invalid file path", fmt.Sprintf("%s: %s", where, err))
		}
		checkCommon(f.Index, f.Trim, f.Sudo, f.Timeout, where)
	}

	for i := range commands {
		c := &commands[i]
		where := fmt.Sprintf("command[%d]", i)
		if !claimKey(c.Key, where) {
			continue
		}
		if c.Command.IsUnknown() {
			diags.AddError("Unknown command", fmt.Sprintf("%s command is not known during this operation.", where))
		} else if c.Command.IsNull() {
			diags.AddError("Missing command", fmt.Sprintf("%s requires a non-empty `command` argv.", where))
		} else {
			var argv []string
			diags.Append(c.Command.ElementsAs(ctx, &argv, false)...)
			if len(argv) == 0 {
				diags.AddError("Missing command", fmt.Sprintf("%s requires a non-empty `command` argv.", where))
			}
			for _, arg := range argv {
				if strings.ContainsRune(arg, 0) {
					diags.AddError("Invalid command argument", fmt.Sprintf("%s contains a NUL byte.", where))
					break
				}
			}
		}
		checkCommon(c.Index, c.Trim, c.Sudo, c.Timeout, where)
	}

	for i := range runs {
		r := &runs[i]
		where := fmt.Sprintf("run[%d]", i)
		if !claimKey(r.Key, where) {
			continue
		}
		if r.Script.IsUnknown() {
			diags.AddError("Unknown script", fmt.Sprintf("%s script is not known during this operation.", where))
		} else if r.Script.IsNull() {
			diags.AddError("Missing script", fmt.Sprintf("%s requires a `script`.", where))
		} else if strings.ContainsRune(r.Script.ValueString(), 0) {
			diags.AddError("Invalid script", fmt.Sprintf("%s script must not contain a NUL byte.", where))
		}
		if r.Shell.IsUnknown() {
			diags.AddError("Unknown shell", fmt.Sprintf("%s shell is not known during this operation.", where))
		} else if !r.Shell.IsNull() && r.Shell.ValueString() != "" {
			if err := ValidateShell(r.Shell.ValueString()); err != nil {
				diags.AddError("Invalid shell", fmt.Sprintf("%s: %s", where, err))
			}
		}
		if r.Envs.IsUnknown() {
			diags.AddError("Unknown envs", fmt.Sprintf("%s envs is not known during this operation.", where))
		} else if !r.Envs.IsNull() {
			var envMap map[string]string
			diags.Append(r.Envs.ElementsAs(ctx, &envMap, false)...)
			for k := range envMap {
				if !client.ValidEnvName(k) {
					diags.AddError("Invalid env name", fmt.Sprintf("%s envs contains invalid variable name %q.", where, k))
				}
			}
		}
		checkCommon(r.Index, r.Trim, r.Sudo, r.Timeout, where)
	}

	if diags.HasError() {
		return client.Config{}, 0, diags
	}
	return cfg, limit, diags
}

// Execute opens one SSH connection, runs every operation in index order, and
// closes the connection. Any failure aborts the remaining operations. The
// caller-supplied sensitive flag suppresses remote stderr and error details in
// diagnostics for every operation.
func (m *OpsModel) Execute(ctx context.Context, sensitive bool) (map[string]string, diag.Diagnostics) {
	var diags diag.Diagnostics
	cfg, limit, d := m.Validate(ctx)
	diags.Append(d...)
	if diags.HasError() {
		return nil, diags
	}

	files, commands, runs, d := m.decodeOperations(ctx)
	diags.Append(d...)
	if diags.HasError() {
		return nil, diags
	}

	connClient, err := client.Dial(ctx, cfg)
	if err != nil {
		diags.AddError("SSH connection failed", err.Error())
		return nil, diags
	}
	defer func() { _ = connClient.Close() }()

	fs := &remote.FS{Client: connClient, Timeout: cfg.Timeout}
	values := map[string]string{}

	ops := make([]plannedOperation, 0, len(files)+len(commands)+len(runs))
	for i := range files {
		idx := int64(0)
		if !files[i].Index.IsNull() {
			idx = files[i].Index.ValueInt64()
		}
		ops = append(ops, plannedOperation{index: idx, kind: fileOperation, order: i, file: &files[i]})
	}
	for i := range commands {
		idx := int64(0)
		if !commands[i].Index.IsNull() {
			idx = commands[i].Index.ValueInt64()
		}
		ops = append(ops, plannedOperation{index: idx, kind: commandOperation, order: i, cmd: &commands[i]})
	}
	for i := range runs {
		idx := int64(0)
		if !runs[i].Index.IsNull() {
			idx = runs[i].Index.ValueInt64()
		}
		ops = append(ops, plannedOperation{index: idx, kind: runOperation, order: i, run: &runs[i]})
	}
	// Stable order: index, kind, declaration order. Same-index operations keep
	// a deterministic provider order.
	for i := 1; i < len(ops); i++ {
		for j := i; j > 0; j-- {
			if operationLess(ops[j], ops[j-1]) {
				ops[j], ops[j-1] = ops[j-1], ops[j]
			} else {
				break
			}
		}
	}

	for _, op := range ops {
		var (
			out       string
			key       string
			trim      bool
			opErr     error
			scriptRes *client.ExecResult
		)
		switch op.kind {
		case fileOperation:
			f := op.file
			timeout, err := durationFrom(f.Timeout, cfg.Timeout.String(), "timeout")
			if err != nil {
				diags.AddError("Invalid timeout", err.Error())
				return nil, diags
			}
			fs.Timeout = timeout
			key = f.Key.ValueString()
			trim = effectiveTrim(f.Trim)
			fs.AllowSudo = effectiveBool(m.Sudo, f.Sudo)
			st, statErr := fs.Stat(ctx, f.Path.ValueString())
			switch {
			case statErr != nil:
				opErr = describeOpError(statErr, sensitive, nil)
			case st.Type != remote.File:
				opErr = fmt.Errorf("path %q is a %s, not a regular file", f.Path.ValueString(), st.Type)
			default:
				data, readErr := fs.ReadFile(ctx, f.Path.ValueString(), limit)
				if readErr != nil {
					opErr = describeOpError(readErr, sensitive, nil)
				} else if !utf8.Valid(data) {
					opErr = errors.New("file content is not valid UTF-8")
				} else {
					out = string(data)
				}
			}
		case commandOperation:
			c := op.cmd
			timeout, err := durationFrom(c.Timeout, cfg.Timeout.String(), "timeout")
			if err != nil {
				diags.AddError("Invalid timeout", err.Error())
				return nil, diags
			}
			fs.Timeout = timeout
			key = c.Key.ValueString()
			trim = effectiveTrim(c.Trim)
			var argv []string
			diags.Append(c.Command.ElementsAs(ctx, &argv, false)...)
			if diags.HasError() {
				return nil, diags
			}
			res, runErr := connClient.Run(ctx, client.ExecOptions{
				Args:      argv,
				Envs:      cfg.Envs,
				Sudo:      effectiveBool(m.Sudo, c.Sudo),
				Timeout:   timeout,
				MaxStdout: int(limit + 1),
				MaxStderr: 16 * 1024,
			})
			if runErr != nil {
				opErr = describeOpError(runErr, sensitive, res)
			} else if res.StdoutTruncated || int64(len(res.Stdout)) > limit {
				opErr = client.ErrOutputLimit
			} else if !utf8.Valid(res.Stdout) {
				opErr = errors.New("command stdout is not valid UTF-8")
			} else {
				out = string(res.Stdout)
			}
		case runOperation:
			r := op.run
			timeout, err := durationFrom(r.Timeout, cfg.Timeout.String(), "timeout")
			if err != nil {
				diags.AddError("Invalid timeout", err.Error())
				return nil, diags
			}
			fs.Timeout = timeout
			key = r.Key.ValueString()
			trim = effectiveTrim(r.Trim)
			envs := map[string]string{}
			for k, v := range cfg.Envs {
				envs[k] = v
			}
			if !r.Envs.IsNull() && !r.Envs.IsUnknown() {
				var extra map[string]string
				diags.Append(r.Envs.ElementsAs(ctx, &extra, false)...)
				for k, v := range extra {
					envs[k] = v
				}
			}
			if diags.HasError() {
				return nil, diags
			}
			out, scriptRes, opErr = executeScript(ctx, connClient, r, envs, effectiveBool(m.Sudo, r.Sudo), timeout, limit)
			if opErr != nil {
				opErr = describeOpError(opErr, sensitive, scriptRes)
			} else if !utf8.ValidString(out) {
				opErr = errors.New("script stdout is not valid UTF-8")
			}
		}
		if opErr != nil {
			diags.AddError("SSH operation failed", fmt.Sprintf("key %q: %s", key, opErr.Error()))
			return nil, diags
		}
		if trim {
			out = strings.TrimSpace(out)
		}
		values[key] = out
	}

	return values, diags
}

func operationLess(a, b plannedOperation) bool {
	if a.index != b.index {
		return a.index < b.index
	}
	if a.kind != b.kind {
		return a.kind < b.kind
	}
	return a.order < b.order
}

func executeScript(ctx context.Context, c *client.Client, r *RunOperationModel, envs map[string]string, sudo bool, timeout time.Duration, limit int64) (string, *client.ExecResult, error) {
	shell := "sh"
	if !r.Shell.IsNull() && r.Shell.ValueString() != "" {
		shell = r.Shell.ValueString()
	}
	// Feed the script through stdin to the configured interpreter. Nothing is
	// written to the remote filesystem, so a timeout or cancelled connection
	// cannot leave a script behind, and the script body does not appear in the
	// remote process argv.
	res, err := c.Run(ctx, client.ExecOptions{
		Args:      []string{shell, "-s"},
		Envs:      envs,
		Sudo:      sudo,
		Stdin:     []byte(r.Script.ValueString()),
		Timeout:   timeout,
		MaxStdout: int(limit + 1),
		MaxStderr: 16 * 1024,
	})
	if err != nil {
		return "", res, err
	}
	if res.StdoutTruncated || int64(len(res.Stdout)) > limit {
		return "", res, client.ErrOutputLimit
	}
	return string(res.Stdout), res, nil
}

func effectiveBool(top, op types.Bool) bool {
	if !op.IsNull() && !op.IsUnknown() {
		return op.ValueBool()
	}
	if !top.IsNull() && !top.IsUnknown() {
		return top.ValueBool()
	}
	return false
}

// effectiveTrim applies the documented default: an omitted trim means true.
func effectiveTrim(v types.Bool) bool {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueBool()
	}
	return true
}

func durationFrom(v types.String, def, name string) (time.Duration, error) {
	if v.IsUnknown() {
		return 0, fmt.Errorf("`%s` is not known at apply time", name)
	}
	s := def
	if !v.IsNull() && v.ValueString() != "" {
		s = v.ValueString()
	}
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("`%s` must be a valid duration: %s", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("`%s` must be positive", name)
	}
	return d, nil
}

func maxOutputSize() (int64, error) {
	raw := os.Getenv(outputLimitEnv)
	if raw == "" {
		return client.DefaultMaxOutput, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer number of bytes", outputLimitEnv)
	}
	return n, nil
}

func describeOpError(err error, sensitive bool, res *client.ExecResult) error {
	if err == nil {
		return nil
	}
	if sensitive {
		switch {
		case errors.Is(err, client.ErrTimeout):
			return errors.New("remote operation timed out")
		case errors.Is(err, client.ErrOutputLimit):
			return fmt.Errorf("remote output exceeded the configured limit of %d bytes", mustOutputLimit())
		default:
			var exitErr *client.ExitError
			if errors.As(err, &exitErr) {
				return fmt.Errorf("remote command exited with status %d", exitErr.Code)
			}
			return errors.New("remote operation failed; details suppressed because the result is sensitive")
		}
	}
	if errors.Is(err, client.ErrOutputLimit) {
		return fmt.Errorf("remote output exceeded the configured limit of %d bytes", mustOutputLimit())
	}
	if errors.Is(err, client.ErrTimeout) {
		return errors.New("remote operation timed out")
	}
	if res != nil && len(res.Stderr) > 0 {
		stderr := strings.TrimSpace(string(res.Stderr))
		if len(stderr) > 1024 {
			stderr = stderr[:1024] + "..."
		}
		var exitErr *client.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("remote command exited with status %d: %s", exitErr.Code, stderr)
		}
		return fmt.Errorf("%s: %s", err, stderr)
	}
	return err
}

func mustOutputLimit() int64 {
	n, err := maxOutputSize()
	if err != nil {
		return client.DefaultMaxOutput
	}
	return n
}

// ValidateManagedPath enforces a cleaned absolute path other than /.
func ValidateManagedPath(p string) error {
	if p == "" {
		return errors.New("path must not be empty")
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("path %q must be absolute", p)
	}
	if strings.ContainsRune(p, 0) {
		return errors.New("path must not contain NUL")
	}
	if path.Clean(p) != p {
		return fmt.Errorf("path %q must be a cleaned absolute path", p)
	}
	if p == "/" {
		return errors.New("path must not be the filesystem root")
	}
	return nil
}

// ValidateShell restricts the script interpreter to a single executable name
// or path without shell metacharacters.
func ValidateShell(shell string) error {
	if shell == "" {
		return errors.New("shell must not be empty")
	}
	if strings.HasPrefix(shell, "-") {
		return errors.New("shell must not start with '-'")
	}
	for _, r := range shell {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.', r == '/':
		default:
			return fmt.Errorf("shell %q contains invalid characters", shell)
		}
	}
	return nil
}

// ApplyTrimDefaults records the documented default for omitted `trim` flags so
// plan and state carry a stable, known value instead of null.
func (m *OpsModel) ApplyTrimDefaults() diag.Diagnostics {
	var diags diag.Diagnostics
	var d diag.Diagnostics
	m.Files, d = withTrimDefaults(m.Files, fileOperationAttrTypes())
	diags.Append(d...)
	m.Commands, d = withTrimDefaults(m.Commands, commandOperationAttrTypes())
	diags.Append(d...)
	m.Runs, d = withTrimDefaults(m.Runs, runOperationAttrTypes())
	diags.Append(d...)
	return diags
}

func withTrimDefaults(list types.List, attrTypes map[string]attr.Type) (types.List, diag.Diagnostics) {
	if list.IsNull() || list.IsUnknown() {
		return list, nil
	}
	var diags diag.Diagnostics
	elements := list.Elements()
	normalized := make([]attr.Value, len(elements))
	for i, element := range elements {
		object, ok := element.(types.Object)
		if !ok {
			diags.AddError("Invalid operation model", fmt.Sprintf("operation %d is %T, not an object.", i, element))
			return list, diags
		}
		attrs := object.Attributes()
		if attrs["trim"].IsNull() {
			attrs["trim"] = types.BoolValue(true)
			normalized[i] = types.ObjectValueMust(attrTypes, attrs)
		} else {
			normalized[i] = object
		}
	}
	return types.ListValue(types.ObjectType{AttrTypes: attrTypes}, normalized)
}

// ValuesMap converts a result map into a Terraform map value.
func ValuesMap(values map[string]string) (types.Map, diag.Diagnostics) {
	if values == nil {
		values = map[string]string{}
	}
	attrs := make(map[string]attr.Value, len(values))
	for k, v := range values {
		attrs[k] = types.StringValue(v)
	}
	return types.MapValue(types.StringType, attrs)
}
