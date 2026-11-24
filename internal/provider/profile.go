package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/geektr-cloud/terraform-provider-ssh/internal/client"
)

// Built-in Connection Profile defaults. They are the base layer beneath
// provider defaults and the Connection Override.
const (
	builtinType       = "linux"
	builtinPort       = client.DefaultPort
	builtinTimeout    = "5m"
	builtinRetryDelay = "5s"
	builtinKnownHosts = "~/.ssh/known_hosts"
)

// stringField is a tri-state string: absent, explicit null, or a value.
type stringField struct {
	Set   bool
	Null  bool
	Value string
}

func stringValue(v string) stringField { return stringField{Set: true, Value: v} }
func stringNull() stringField          { return stringField{Set: true, Null: true} }

// boolField is a tri-state bool.
type boolField struct {
	Set   bool
	Null  bool
	Value bool
}

func boolValue(v bool) boolField { return boolField{Set: true, Value: v} }
func boolNull() boolField        { return boolField{Set: true, Null: true} }

// envField distinguishes an absent envs member, an explicit null, and a map
// merge. Values is nil only when Null is true.
type envField struct {
	Set    bool
	Null   bool
	Values map[string]string
}

// jumpField distinguishes an absent jump, an explicit null, and a replacement
// profile.
type jumpField struct {
	Set   bool
	Null  bool
	Value JumpProfile
}

// JumpProfile is the single-layer jump host part of a Connection Profile.
type JumpProfile struct {
	Host                  stringField
	Port                  stringField
	User                  stringField
	PrivateKey            stringField
	KnownHosts            stringField
	HostKey               stringField
	InsecureIgnoreHostKey boolField
}

// Profile is a partial Connection Profile. Absent fields inherit; explicit
// nulls clear; values replace or merge according to the field.
type Profile struct {
	Type                  stringField
	Host                  stringField
	Port                  stringField
	User                  stringField
	PrivateKey            stringField
	KnownHosts            stringField
	HostKey               stringField
	InsecureIgnoreHostKey boolField
	Envs                  envField
	Timeout               stringField
	RetryDelay            stringField
	Jump                  jumpField
}

// providerDefaults is the provider-level Connection Profile.
type providerDefaults struct {
	profile Profile
}

func defaultsProfile(d *providerDefaults) Profile {
	if d == nil {
		return Profile{}
	}
	return d.profile
}

// mergeProfile applies overlay on base. Absent overlay fields inherit.
func mergeProfile(base, overlay Profile) Profile {
	out := base
	if overlay.Type.Set {
		out.Type = overlay.Type
	}
	if overlay.Host.Set {
		out.Host = overlay.Host
	}
	if overlay.Port.Set {
		out.Port = overlay.Port
	}
	if overlay.User.Set {
		out.User = overlay.User
	}
	if overlay.PrivateKey.Set {
		out.PrivateKey = overlay.PrivateKey
	}
	if overlay.KnownHosts.Set {
		out.KnownHosts = overlay.KnownHosts
	}
	if overlay.HostKey.Set {
		out.HostKey = overlay.HostKey
	}
	if overlay.InsecureIgnoreHostKey.Set {
		out.InsecureIgnoreHostKey = overlay.InsecureIgnoreHostKey
	}
	if overlay.Timeout.Set {
		out.Timeout = overlay.Timeout
	}
	if overlay.RetryDelay.Set {
		out.RetryDelay = overlay.RetryDelay
	}
	if overlay.Envs.Set {
		if overlay.Envs.Null {
			out.Envs = envField{Set: true, Null: true}
		} else {
			merged := map[string]string{}
			if out.Envs.Set && !out.Envs.Null {
				for k, v := range out.Envs.Values {
					merged[k] = v
				}
			}
			for k, v := range overlay.Envs.Values {
				merged[k] = v
			}
			out.Envs = envField{Set: true, Values: merged}
		}
	}
	if overlay.Jump.Set {
		out.Jump = overlay.Jump
	}
	return out
}

func fieldValue(f stringField) (string, bool) {
	if !f.Set || f.Null {
		return "", false
	}
	return f.Value, true
}

func boolFieldValue(f boolField) (bool, bool) {
	if !f.Set || f.Null {
		return false, false
	}
	return f.Value, true
}

func splitPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// validateProfile performs configuration-only validation. It accepts a partial
// profile: host, user, and private_key may be absent, but values that are
// present must be valid, and host key modes must not conflict.
func validateProfile(p Profile, prefix string) error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	validateText := func(f stringField, name string, allowEmpty bool) {
		path := splitPath(prefix, name)
		v, ok := fieldValue(f)
		if !ok {
			return
		}
		if !allowEmpty && strings.TrimSpace(v) == "" {
			add("%s must not be empty", path)
			return
		}
		if strings.ContainsAny(v, "\r\n\x00") && (name == "host" || name == "user") {
			add("%s contains invalid characters", path)
		}
	}

	validateText(p.Type, "type", false)
	if v, ok := fieldValue(p.Type); ok && v != builtinType {
		add("%s must be %q", splitPath(prefix, "type"), builtinType)
	}
	validateText(p.Host, "host", false)
	validateText(p.User, "user", false)
	validateText(p.PrivateKey, "private_key", false)
	validateText(p.KnownHosts, "known_hosts", false)
	validateText(p.HostKey, "host_key", false)

	if v, ok := fieldValue(p.Port); ok {
		if err := validatePort(v); err != nil {
			add("%s: %s", splitPath(prefix, "port"), err)
		}
	}
	if v, ok := fieldValue(p.Timeout); ok {
		if err := validateDuration(v); err != nil {
			add("%s: %s", splitPath(prefix, "timeout"), err)
		}
	}
	if v, ok := fieldValue(p.RetryDelay); ok {
		if err := validateDuration(v); err != nil {
			add("%s: %s", splitPath(prefix, "retry_delay"), err)
		}
	}
	if p.Envs.Set && !p.Envs.Null {
		for k := range p.Envs.Values {
			if !client.ValidEnvName(k) {
				add("%s contains invalid variable name %q", splitPath(prefix, "envs"), k)
			}
		}
	}
	if err := validateHostKeyModes(p.KnownHosts, p.HostKey, p.InsecureIgnoreHostKey, prefix); err != nil {
		problems = append(problems, err.Error())
	}

	if p.Jump.Set && !p.Jump.Null {
		j := p.Jump.Value
		jp := splitPath(prefix, "jump")
		for _, f := range []struct {
			field stringField
			name  string
		}{
			{j.Host, "host"},
			{j.User, "user"},
			{j.PrivateKey, "private_key"},
			{j.KnownHosts, "known_hosts"},
			{j.HostKey, "host_key"},
		} {
			v, ok := fieldValue(f.field)
			if !ok {
				continue
			}
			if strings.TrimSpace(v) == "" {
				add("%s must not be empty", splitPath(jp, f.name))
			}
			if strings.ContainsAny(v, "\r\n\x00") && (f.name == "host" || f.name == "user") {
				add("%s contains invalid characters", splitPath(jp, f.name))
			}
		}
		if v, ok := fieldValue(j.Port); ok {
			if err := validatePort(v); err != nil {
				add("%s: %s", splitPath(jp, "port"), err)
			}
		}
		if err := validateHostKeyModes(j.KnownHosts, j.HostKey, j.InsecureIgnoreHostKey, jp); err != nil {
			problems = append(problems, err.Error())
		}
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validatePort(v string) error {
	p, err := strconv.Atoi(v)
	if err != nil || p < 1 || p > 65535 {
		return errors.New("must be an integer between 1 and 65535")
	}
	return nil
}

func validateDuration(v string) error {
	d, err := time.ParseDuration(v)
	if err != nil {
		return errors.New("must be a valid duration such as `5m`")
	}
	if d <= 0 {
		return errors.New("must be positive")
	}
	return nil
}

func validateHostKeyModes(knownHosts, hostKey stringField, insecure boolField, prefix string) error {
	modes := 0
	var names []string
	if _, ok := fieldValue(knownHosts); ok {
		modes++
		names = append(names, "known_hosts")
	}
	if _, ok := fieldValue(hostKey); ok {
		modes++
		names = append(names, "host_key")
	}
	if v, ok := boolFieldValue(insecure); ok && v {
		modes++
		names = append(names, "insecure_ignore_host_key")
	}
	if modes > 1 {
		return fmt.Errorf("%s and %s are mutually exclusive", splitPath(prefix, names[0]), splitPath(prefix, names[1]))
	}
	return nil
}

// selectHostKey applies the documented host key mode rules. An explicit
// known_hosts or host_key in the override selects that mode and clears the
// other provider mode. An explicit insecure = true selects the insecure mode.
// An explicit insecure = false does not clear a provider-fixed mode.
func selectHostKey(merged Profile, override Profile) (knownHosts, hostKey string, insecure bool) {
	if v, ok := fieldValue(override.KnownHosts); ok {
		return v, "", false
	}
	if v, ok := fieldValue(override.HostKey); ok {
		return "", v, false
	}
	if v, ok := boolFieldValue(override.InsecureIgnoreHostKey); ok && v {
		return "", "", true
	}
	knownHosts, _ = fieldValue(merged.KnownHosts)
	hostKey, _ = fieldValue(merged.HostKey)
	if v, ok := boolFieldValue(merged.InsecureIgnoreHostKey); ok {
		insecure = v
	}
	if knownHosts == "" && hostKey == "" && !insecure {
		knownHosts = builtinKnownHosts
	}
	return knownHosts, hostKey, insecure
}

func resolveJumpProfile(j JumpProfile) (client.Endpoint, error) {
	var problems []string
	endpoint := client.Endpoint{}
	host, ok := fieldValue(j.Host)
	if !ok || strings.TrimSpace(host) == "" {
		problems = append(problems, "jump.host is required")
	}
	endpoint.Host = host
	port := builtinPort
	if v, ok := fieldValue(j.Port); ok {
		port = v
	}
	endpoint.Port = port
	user, ok := fieldValue(j.User)
	if !ok || strings.TrimSpace(user) == "" {
		problems = append(problems, "jump.user is required")
	}
	endpoint.User = user
	key, ok := fieldValue(j.PrivateKey)
	if !ok || strings.TrimSpace(key) == "" {
		problems = append(problems, "jump.private_key is required")
	}
	endpoint.PrivateKey = key

	// Jump profiles are replaced as a whole, so its own explicit modes are the
	// only modes to consider.
	if v, ok := fieldValue(j.KnownHosts); ok {
		endpoint.KnownHosts = v
	} else if v, ok := fieldValue(j.HostKey); ok {
		endpoint.HostKey = v
	} else if v, ok := boolFieldValue(j.InsecureIgnoreHostKey); ok && v {
		endpoint.InsecureIgnoreHostKey = true
	} else {
		endpoint.KnownHosts = builtinKnownHosts
	}

	if len(problems) > 0 {
		return client.Endpoint{}, errors.New(strings.Join(problems, "; "))
	}
	return endpoint, nil
}

// resolveConnection merges provider defaults with the override, applies
// built-in defaults, and builds the client configuration.
func resolveConnection(provider Profile, override Profile) (client.Config, error) {
	merged := mergeProfile(provider, override)

	var problems []string
	host, ok := fieldValue(merged.Host)
	if !ok || strings.TrimSpace(host) == "" {
		problems = append(problems, "host is required: set it on the provider or in the conn JSON override")
	}
	user, ok := fieldValue(merged.User)
	if !ok || strings.TrimSpace(user) == "" {
		problems = append(problems, "user is required: set it on the provider or in the conn JSON override")
	}
	privateKey, ok := fieldValue(merged.PrivateKey)
	if !ok || strings.TrimSpace(privateKey) == "" {
		problems = append(problems, "private_key is required: set it on the provider or in the conn JSON override")
	}
	typ := builtinType
	if v, ok := fieldValue(merged.Type); ok {
		typ = v
	}
	if typ != builtinType {
		problems = append(problems, fmt.Sprintf("type must be %q", builtinType))
	}
	port := builtinPort
	if v, ok := fieldValue(merged.Port); ok {
		port = v
	}
	if err := validatePort(port); err != nil {
		problems = append(problems, "port: "+err.Error())
	}
	timeoutText := builtinTimeout
	if v, ok := fieldValue(merged.Timeout); ok {
		timeoutText = v
	}
	timeout, err := time.ParseDuration(timeoutText)
	if err != nil || timeout <= 0 {
		problems = append(problems, "timeout: must be a valid positive duration such as `5m`")
	}
	retryText := builtinRetryDelay
	if v, ok := fieldValue(merged.RetryDelay); ok {
		retryText = v
	}
	retryDelay, err := time.ParseDuration(retryText)
	if err != nil || retryDelay <= 0 {
		problems = append(problems, "retry_delay: must be a valid positive duration such as `5s`")
	}
	if len(problems) > 0 {
		return client.Config{}, errors.New(strings.Join(problems, "; "))
	}

	knownHosts, hostKey, insecure := selectHostKey(merged, override)

	envs := map[string]string{}
	if merged.Envs.Set && !merged.Envs.Null {
		for k, v := range merged.Envs.Values {
			envs[k] = v
		}
	}

	cfg := client.Config{
		Endpoint: client.Endpoint{
			Host:                  host,
			Port:                  port,
			User:                  user,
			PrivateKey:            privateKey,
			KnownHosts:            knownHosts,
			HostKey:               hostKey,
			InsecureIgnoreHostKey: insecure,
		},
		Envs:       envs,
		Timeout:    timeout,
		RetryDelay: retryDelay,
	}

	jump := merged.Jump
	if jump.Set && !jump.Null {
		resolved, err := resolveJumpProfile(jump.Value)
		if err != nil {
			return client.Config{}, err
		}
		cfg.Jump = &resolved
	}

	if err := cfg.Validate(); err != nil {
		return client.Config{}, fmt.Errorf("invalid connection profile: %w", err)
	}
	return cfg, nil
}

// connectionIdentity is the canonical non-sensitive identity of an endpoint.
type connectionIdentity struct {
	Host string                    `json:"host"`
	Port string                    `json:"port"`
	User string                    `json:"user"`
	Jump *connectionIdentityTarget `json:"jump,omitempty"`
}

type connectionIdentityTarget struct {
	Host string `json:"host"`
	Port string `json:"port"`
	User string `json:"user"`
}

// resolveIdentity returns the canonical Connection Identity for the merged
// profile. ok is false when a required identity field is still absent, which
// callers must treat conservatively (never as an empty string).
func resolveIdentity(provider Profile, override Profile) (string, bool, error) {
	merged := mergeProfile(provider, override)
	host, ok := fieldValue(merged.Host)
	if !ok || strings.TrimSpace(host) == "" {
		return "", false, nil
	}
	user, ok := fieldValue(merged.User)
	if !ok || strings.TrimSpace(user) == "" {
		return "", false, nil
	}
	port := builtinPort
	if v, ok := fieldValue(merged.Port); ok {
		port = v
	}
	identity := connectionIdentity{Host: host, Port: port, User: user}

	if merged.Jump.Set && !merged.Jump.Null {
		j := merged.Jump.Value
		jHost, ok := fieldValue(j.Host)
		if !ok || strings.TrimSpace(jHost) == "" {
			return "", false, nil
		}
		jUser, ok := fieldValue(j.User)
		if !ok || strings.TrimSpace(jUser) == "" {
			return "", false, nil
		}
		jPort := builtinPort
		if v, ok := fieldValue(j.Port); ok {
			jPort = v
		}
		identity.Jump = &connectionIdentityTarget{Host: jHost, Port: jPort, User: jUser}
	}

	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", false, fmt.Errorf("encode connection identity: %w", err)
	}
	return string(encoded), true, nil
}

// parseConnOverride parses a strict JSON Connection Override. Diagnostics
// contain only field paths and error types; raw JSON and values are never
// included.
func parseConnOverride(raw string) (Profile, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	profile, isNull, err := parseProfileObject(dec, "")
	if err != nil {
		return Profile{}, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return Profile{}, errors.New("conn: unexpected data after the JSON object")
	}
	if isNull {
		return Profile{}, nil
	}
	if err := validateProfile(profile, "conn"); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// parseProfileObject consumes one object or null. isNull reports an explicit
// JSON null at the top level.
func parseProfileObject(dec *json.Decoder, prefix string) (Profile, bool, error) {
	var profile Profile
	tok, err := dec.Token()
	if err != nil {
		return profile, false, errors.New("conn: invalid JSON")
	}
	if tok == nil {
		return profile, true, nil
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return profile, false, fmt.Errorf("conn: expected an object or null, got %s", tokenKind(tok))
	}
	seen := map[string]bool{}
	for dec.More() {
		keyValue, err := dec.Token()
		if err != nil {
			return profile, false, errors.New("conn: invalid JSON")
		}
		key, ok := keyValue.(string)
		if !ok {
			return profile, false, errors.New("conn: object key is not a string")
		}
		path := splitPath(prefix, key)
		if seen[key] {
			return profile, false, fmt.Errorf("conn: duplicate key %q", path)
		}
		seen[key] = true
		if err := parseProfileField(dec, key, path, &profile); err != nil {
			return profile, false, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return profile, false, errors.New("conn: invalid JSON")
	}
	return profile, false, nil
}

func parseProfileField(dec *json.Decoder, key, path string, profile *Profile) error {
	switch key {
	case "type":
		return parseStringInto(dec, path, &profile.Type)
	case "host":
		return parseStringInto(dec, path, &profile.Host)
	case "port":
		return parseStringInto(dec, path, &profile.Port)
	case "user":
		return parseStringInto(dec, path, &profile.User)
	case "private_key":
		return parseStringInto(dec, path, &profile.PrivateKey)
	case "known_hosts":
		return parseStringInto(dec, path, &profile.KnownHosts)
	case "host_key":
		return parseStringInto(dec, path, &profile.HostKey)
	case "insecure_ignore_host_key":
		return parseBoolInto(dec, path, &profile.InsecureIgnoreHostKey)
	case "timeout":
		return parseStringInto(dec, path, &profile.Timeout)
	case "retry_delay":
		return parseStringInto(dec, path, &profile.RetryDelay)
	case "envs":
		return parseEnvs(dec, path, &profile.Envs)
	case "jump":
		return parseJump(dec, path, &profile.Jump)
	default:
		return fmt.Errorf("conn: unknown field %q", path)
	}
}

func parseStringInto(dec *json.Decoder, path string, target *stringField) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.New("conn: invalid JSON")
	}
	if tok == nil {
		*target = stringNull()
		return nil
	}
	s, ok := tok.(string)
	if !ok {
		return fmt.Errorf("conn: field %q must be a string, got %s", path, tokenKind(tok))
	}
	*target = stringValue(s)
	return nil
}

func parseBoolInto(dec *json.Decoder, path string, target *boolField) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.New("conn: invalid JSON")
	}
	if tok == nil {
		*target = boolNull()
		return nil
	}
	b, ok := tok.(bool)
	if !ok {
		return fmt.Errorf("conn: field %q must be a boolean, got %s", path, tokenKind(tok))
	}
	*target = boolValue(b)
	return nil
}

func parseEnvs(dec *json.Decoder, path string, target *envField) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.New("conn: invalid JSON")
	}
	if tok == nil {
		*target = envField{Set: true, Null: true}
		return nil
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("conn: field %q must be an object or null, got %s", path, tokenKind(tok))
	}
	values := map[string]string{}
	seen := map[string]bool{}
	for dec.More() {
		keyValue, err := dec.Token()
		if err != nil {
			return errors.New("conn: invalid JSON")
		}
		key, ok := keyValue.(string)
		if !ok {
			return errors.New("conn: envs key is not a string")
		}
		if seen[key] {
			return fmt.Errorf("conn: duplicate key %q", path+"."+key)
		}
		seen[key] = true
		valueTok, err := dec.Token()
		if err != nil {
			return errors.New("conn: invalid JSON")
		}
		if valueTok == nil {
			return fmt.Errorf("conn: field %q must be a string, got null", path+"."+key)
		}
		value, ok := valueTok.(string)
		if !ok {
			return fmt.Errorf("conn: field %q must be a string, got %s", path+"."+key, tokenKind(valueTok))
		}
		values[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return errors.New("conn: invalid JSON")
	}
	*target = envField{Set: true, Values: values}
	return nil
}

func parseJump(dec *json.Decoder, path string, target *jumpField) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.New("conn: invalid JSON")
	}
	if tok == nil {
		*target = jumpField{Set: true, Null: true}
		return nil
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("conn: field %q must be an object or null, got %s", path, tokenKind(tok))
	}
	var jump JumpProfile
	seen := map[string]bool{}
	for dec.More() {
		keyValue, err := dec.Token()
		if err != nil {
			return errors.New("conn: invalid JSON")
		}
		key, ok := keyValue.(string)
		if !ok {
			return errors.New("conn: jump key is not a string")
		}
		keyPath := path + "." + key
		if seen[key] {
			return fmt.Errorf("conn: duplicate key %q", keyPath)
		}
		seen[key] = true
		switch key {
		case "host":
			err = parseStringInto(dec, keyPath, &jump.Host)
		case "port":
			err = parseStringInto(dec, keyPath, &jump.Port)
		case "user":
			err = parseStringInto(dec, keyPath, &jump.User)
		case "private_key":
			err = parseStringInto(dec, keyPath, &jump.PrivateKey)
		case "known_hosts":
			err = parseStringInto(dec, keyPath, &jump.KnownHosts)
		case "host_key":
			err = parseStringInto(dec, keyPath, &jump.HostKey)
		case "insecure_ignore_host_key":
			err = parseBoolInto(dec, keyPath, &jump.InsecureIgnoreHostKey)
		default:
			return fmt.Errorf("conn: unknown field %q", keyPath)
		}
		if err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return errors.New("conn: invalid JSON")
	}
	*target = jumpField{Set: true, Value: jump}
	return nil
}

func tokenKind(tok json.Token) string {
	switch tok.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case json.Delim:
		if tok == json.Delim('{') {
			return "object"
		}
		return "array"
	default:
		return "unknown value"
	}
}

// resolveConnConfig resolves a consumer's conn value with the current provider
// defaults. An unknown conn value is rejected because every remote operation
// requires a known configuration.
func resolveConnConfig(provider Profile, raw types.String) (client.Config, error) {
	if raw.IsUnknown() {
		return client.Config{}, errors.New("conn is not known during this operation")
	}
	var override Profile
	if !raw.IsNull() {
		parsed, err := parseConnOverride(raw.ValueString())
		if err != nil {
			return client.Config{}, err
		}
		override = parsed
	}
	return resolveConnection(provider, override)
}

// resolveConnIdentity resolves the Connection Identity for a consumer's conn
// value. ok is false when a required identity field is unknown, absent, or the
// whole conn value is unknown.
func resolveConnIdentity(provider Profile, raw types.String) (string, bool, error) {
	if raw.IsUnknown() {
		return "", false, nil
	}
	var override Profile
	if !raw.IsNull() {
		parsed, err := parseConnOverride(raw.ValueString())
		if err != nil {
			return "", false, err
		}
		override = parsed
	}
	return resolveIdentity(provider, override)
}

// checkConnSyntax parses a known conn value without requiring a complete
// profile. It is used at plan time.
func checkConnSyntax(raw types.String) error {
	if raw.IsNull() || raw.IsUnknown() {
		return nil
	}
	_, err := parseConnOverride(raw.ValueString())
	return err
}
