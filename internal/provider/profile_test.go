package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	providerschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestParseConnOverrideStrict(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
		absent  bool
	}{
		{name: "null", raw: "null", absent: true},
		{name: "empty object", raw: "{}", absent: true},
		{name: "explicit null field", raw: `{"host":null}`},
		{name: "valid object", raw: `{"host":"h","port":"22","user":"u","private_key":"k","insecure_ignore_host_key":true}`},
		{name: "array", raw: `[1]`, wantErr: "object or null, got array"},
		{name: "scalar", raw: `42`, wantErr: "object or null, got number"},
		{name: "string", raw: `"x"`, wantErr: "object or null, got string"},
		{name: "trailing data", raw: `{} {}`, wantErr: "unexpected data"},
		{name: "unknown field", raw: `{"nope":1}`, wantErr: `unknown field "nope"`},
		{name: "duplicate top-level key", raw: `{"host":"a","host":"b"}`, wantErr: `duplicate key "host"`},
		{name: "duplicate jump key", raw: `{"jump":{"host":"a","host":"b"}}`, wantErr: `duplicate key "jump.host"`},
		{name: "duplicate envs key", raw: `{"envs":{"A":"1","A":"2"}}`, wantErr: `duplicate key "envs.A"`},
		{name: "wrong string type", raw: `{"host":42}`, wantErr: `field "host" must be a string, got number`},
		{name: "wrong bool type", raw: `{"insecure_ignore_host_key":"true"}`, wantErr: `must be a boolean, got string`},
		{name: "envs array", raw: `{"envs":[]}`, wantErr: `field "envs" must be an object or null, got array`},
		{name: "envs null value", raw: `{"envs":{"A":null}}`, wantErr: `field "envs.A" must be a string, got null`},
		{name: "jump nested jump", raw: `{"jump":{"jump":{}}}`, wantErr: `unknown field "jump.jump"`},
		{name: "jump envs", raw: `{"jump":{"envs":{}}}`, wantErr: `unknown field "jump.envs"`},
		{name: "jump timeout", raw: `{"jump":{"timeout":"1m"}}`, wantErr: `unknown field "jump.timeout"`},
		{name: "jump scalar", raw: `{"jump":"x"}`, wantErr: `field "jump" must be an object or null, got string`},
		{name: "invalid json", raw: `{"host":`, wantErr: "invalid JSON"},
		{name: "host key conflict", raw: `{"known_hosts":"a","host_key":"b"}`, wantErr: "mutually exclusive"},
		{name: "empty host", raw: `{"host":""}`, wantErr: `conn.host must not be empty`},
		{name: "invalid port", raw: `{"port":"70000"}`, wantErr: "between 1 and 65535"},
		{name: "invalid duration", raw: `{"timeout":"soon"}`, wantErr: "valid duration"},
		{name: "invalid env name", raw: `{"envs":{"1BAD":"x"}}`, wantErr: "invalid variable name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			profile, err := parseConnOverride(tc.raw)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.absent && (profile.Host.Set || profile.Port.Set || profile.Jump.Set) {
					t.Fatalf("expected no override fields, got %#v", profile)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestParseConnOverrideNeverEchoesSecrets(t *testing.T) {
	const secret = "SUPER-SECRET-PRIVATE-KEY"
	cases := []string{
		`{"private_key":` + `"` + secret + `"` + `,"host":42}`,
		`{"private_key":["` + secret + `"]}`,
		`{"private_key":"` + secret,
	}
	for _, raw := range cases {
		_, err := parseConnOverride(raw)
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("diagnostic leaked a private value: %q", err.Error())
		}
	}
}

func TestResolveConnectionLayerDefaults(t *testing.T) {
	cfg, err := resolveConnection(Profile{}, Profile{})
	if err == nil {
		t.Fatal("an empty profile must be rejected before dialing")
	}
	for _, want := range []string{"host is required", "user is required", "private_key is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	}
	_ = cfg

	provider := Profile{
		Host:                  stringValue("provider-host"),
		User:                  stringValue("provider-user"),
		PrivateKey:            stringValue("provider-key"),
		Port:                  stringValue("2222"),
		Timeout:               stringValue("1m"),
		RetryDelay:            stringValue("2s"),
		InsecureIgnoreHostKey: boolValue(true),
		Envs:                  envField{Set: true, Values: map[string]string{"A": "1", "B": "2"}},
	}
	resolved, err := resolveConnection(provider, Profile{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Host != "provider-host" || resolved.Port != "2222" || resolved.User != "provider-user" {
		t.Fatalf("inherited target = %+v", resolved.Endpoint)
	}
	if resolved.Timeout.String() != "1m0s" || resolved.RetryDelay.String() != "2s" {
		t.Fatalf("inherited durations = %v %v", resolved.Timeout, resolved.RetryDelay)
	}
	if !resolved.InsecureIgnoreHostKey {
		t.Fatal("provider insecure mode must be inherited")
	}
	if resolved.Envs["A"] != "1" || resolved.Envs["B"] != "2" {
		t.Fatalf("inherited envs = %#v", resolved.Envs)
	}
}

func TestResolveConnectionExplicitNullClearsToBuiltin(t *testing.T) {
	provider := Profile{
		Host:                  stringValue("provider-host"),
		User:                  stringValue("provider-user"),
		PrivateKey:            stringValue("provider-key"),
		Port:                  stringValue("2222"),
		Timeout:               stringValue("1m"),
		Envs:                  envField{Set: true, Values: map[string]string{"A": "1"}},
		InsecureIgnoreHostKey: boolValue(true),
	}
	override, err := parseConnOverride(`{"port":null,"timeout":null,"envs":null}`)
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	resolved, err := resolveConnection(provider, override)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Port != builtinPort {
		t.Fatalf("port = %q, want builtin", resolved.Port)
	}
	if resolved.Timeout.String() != "5m0s" {
		t.Fatalf("timeout = %v, want builtin", resolved.Timeout)
	}
	if len(resolved.Envs) != 0 {
		t.Fatalf("envs = %#v, want cleared", resolved.Envs)
	}
	if !resolved.InsecureIgnoreHostKey {
		t.Fatal("clearing other fields must not clear the provider host key mode")
	}
	if resolved.Host != "provider-host" || resolved.User != "provider-user" {
		t.Fatal("absent target fields must inherit")
	}
}

func TestResolveEnvsMerge(t *testing.T) {
	provider := Profile{
		Host:       stringValue("h"),
		User:       stringValue("u"),
		PrivateKey: stringValue("k"),
		Envs:       envField{Set: true, Values: map[string]string{"A": "1", "B": "2"}},
	}
	override, _ := parseConnOverride(`{"envs":{"B":"x","C":"3"}}`)
	cfg, err := resolveConnection(provider, override)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := map[string]string{"A": "1", "B": "x", "C": "3"}
	for k, v := range want {
		if cfg.Envs[k] != v {
			t.Fatalf("envs[%s] = %q, want %q (all: %#v)", k, cfg.Envs[k], v, cfg.Envs)
		}
	}

	empty, _ := parseConnOverride(`{"envs":{}}`)
	cfg, err = resolveConnection(provider, empty)
	if err != nil {
		t.Fatalf("resolve empty envs: %v", err)
	}
	if len(cfg.Envs) != 2 || cfg.Envs["A"] != "1" || cfg.Envs["B"] != "2" {
		t.Fatalf("empty envs object must not remove provider envs: %#v", cfg.Envs)
	}
}

func TestResolveHostKeyModeSelection(t *testing.T) {
	base := Profile{Host: stringValue("h"), User: stringValue("u"), PrivateKey: stringValue("k")}

	tests := []struct {
		name      string
		provider  Profile
		override  string
		wantKnown string
		wantKey   string
		wantInsec bool
	}{
		{
			name:      "builtin default",
			provider:  base,
			wantKnown: builtinKnownHosts,
		},
		{
			name:      "provider known_hosts",
			provider:  withHostKey(base, stringValue("/tmp/kh"), stringField{}, boolField{}),
			wantKnown: "/tmp/kh",
		},
		{
			name:      "override known_hosts clears provider host_key",
			provider:  withHostKey(base, stringField{}, stringValue("provider-key"), boolField{}),
			override:  `{"known_hosts":"/tmp/override"}`,
			wantKnown: "/tmp/override",
		},
		{
			name:     "override host_key clears provider known_hosts",
			provider: withHostKey(base, stringValue("/tmp/kh"), stringField{}, boolField{}),
			override: `{"host_key":"override-key"}`,
			wantKey:  "override-key",
		},
		{
			name:      "override insecure true clears provider host_key",
			provider:  withHostKey(base, stringField{}, stringValue("provider-key"), boolField{}),
			override:  `{"insecure_ignore_host_key":true}`,
			wantInsec: true,
		},
		{
			name:     "override insecure false keeps provider host_key",
			provider: withHostKey(base, stringField{}, stringValue("provider-key"), boolField{}),
			override: `{"insecure_ignore_host_key":false}`,
			wantKey:  "provider-key",
		},
		{
			name:     "override null known_hosts does not clear provider host_key",
			provider: withHostKey(base, stringField{}, stringValue("provider-key"), boolField{}),
			override: `{"known_hosts":null}`,
			wantKey:  "provider-key",
		},
		{
			name:      "clearing provider known_hosts falls back to default",
			provider:  withHostKey(base, stringValue("/tmp/kh"), stringField{}, boolField{}),
			override:  `{"known_hosts":null}`,
			wantKnown: builtinKnownHosts,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var override Profile
			if tc.override != "" {
				var err error
				override, err = parseConnOverride(tc.override)
				if err != nil {
					t.Fatalf("override: %v", err)
				}
			}
			cfg, err := resolveConnection(tc.provider, override)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if cfg.KnownHosts != tc.wantKnown || cfg.HostKey != tc.wantKey || cfg.InsecureIgnoreHostKey != tc.wantInsec {
				t.Fatalf("host key mode = (%q, %q, %t), want (%q, %q, %t)",
					cfg.KnownHosts, cfg.HostKey, cfg.InsecureIgnoreHostKey, tc.wantKnown, tc.wantKey, tc.wantInsec)
			}
		})
	}
}

func withHostKey(base Profile, knownHosts, hostKey stringField, insecure boolField) Profile {
	out := base
	out.KnownHosts = knownHosts
	out.HostKey = hostKey
	out.InsecureIgnoreHostKey = insecure
	return out
}

func TestResolveJumpReplacesWholeProfile(t *testing.T) {
	provider := Profile{
		Host:       stringValue("h"),
		User:       stringValue("u"),
		PrivateKey: stringValue("k"),
		Jump: jumpField{Set: true, Value: JumpProfile{
			Host:       stringValue("provider-jump"),
			Port:       stringValue("2200"),
			User:       stringValue("provider-jump-user"),
			PrivateKey: stringValue("provider-jump-key"),
			KnownHosts: stringValue("/tmp/provider-kh"),
		}},
	}
	replacement, err := parseConnOverride(`{"jump":{"host":"override-jump","user":"override-user","private_key":"override-key","insecure_ignore_host_key":true}}`)
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	cfg, err := resolveConnection(provider, replacement)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Jump == nil || cfg.Jump.Host != "override-jump" || cfg.Jump.User != "override-user" ||
		cfg.Jump.PrivateKey != "override-key" || !cfg.Jump.InsecureIgnoreHostKey || cfg.Jump.KnownHosts != "" {
		t.Fatalf("jump was not replaced as a whole: %+v", cfg.Jump)
	}

	cleared, _ := parseConnOverride(`{"jump":null}`)
	cfg, err = resolveConnection(provider, cleared)
	if err != nil {
		t.Fatalf("resolve cleared: %v", err)
	}
	if cfg.Jump != nil {
		t.Fatalf("jump = %+v, want none", cfg.Jump)
	}
}

func TestValidateProfileAllowsPartialProvider(t *testing.T) {
	if err := validateProfile(Profile{}, "provider"); err != nil {
		t.Fatalf("empty provider defaults must be valid: %v", err)
	}
	if err := validateProfile(Profile{Host: stringValue("h")}, "provider"); err != nil {
		t.Fatalf("partial provider defaults must be valid: %v", err)
	}
	if err := validateProfile(Profile{Type: stringValue("windows")}, "provider"); err == nil {
		t.Fatal("unsupported type must be rejected")
	}
	if err := validateProfile(Profile{Port: stringValue("0")}, "provider"); err == nil {
		t.Fatal("invalid port must be rejected")
	}
	if err := validateProfile(Profile{
		KnownHosts: stringValue("/tmp/kh"),
		HostKey:    stringValue("key"),
	}, "provider"); err == nil {
		t.Fatal("conflicting provider host key modes must be rejected")
	}
	if err := validateProfile(Profile{
		Jump: jumpField{Set: true, Value: JumpProfile{
			KnownHosts: stringValue("/tmp/kh"),
			HostKey:    stringValue("key"),
		}},
	}, "provider"); err == nil {
		t.Fatal("conflicting jump host key modes must be rejected")
	}
}

func TestProviderSchemaFieldsAreOptional(t *testing.T) {
	p := New("test")()
	var resp provider.SchemaResponse
	p.Schema(context.Background(), provider.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	for name, attribute := range resp.Schema.Attributes {
		if attribute.IsRequired() {
			t.Fatalf("provider attribute %q must be Optional", name)
		}
		if !attribute.IsOptional() {
			t.Fatalf("provider attribute %q must be Optional", name)
		}
	}
	jump, ok := resp.Schema.Attributes["jump"].(providerschema.SingleNestedAttribute)
	if !ok {
		t.Fatal("provider schema must expose jump as a SingleNestedAttribute")
	}
	for name, attribute := range jump.Attributes {
		if attribute.IsRequired() || !attribute.IsOptional() {
			t.Fatalf("provider jump attribute %q must be Optional", name)
		}
	}
}

func TestBuildStateModelKeepsRawOverrideOnly(t *testing.T) {
	ctx := context.Background()
	cfg := sshResourceModel{
		Conn:        types.StringNull(),
		Umask:       types.StringValue("022"),
		Directories: types.ListNull(types.ObjectType{AttrTypes: directoryAttrTypes()}),
		Files:       types.ListNull(types.ObjectType{AttrTypes: fileAttrTypes()}),
	}
	state := buildStateModel(ctx, &cfg, &cfg, types.StringValue(`{"host":"h"}`), nil, 022, nil, nil)
	if !state.Conn.IsNull() {
		t.Fatalf("provider-only resource must not persist a resolved conn, got %q", state.Conn.ValueString())
	}
	if state.ConnectionIdentity.ValueString() != `{"host":"h"}` {
		t.Fatalf("identity = %q", state.ConnectionIdentity.ValueString())
	}

	raw := `{"host":"h","user":"u","private_key":"json-key"}`
	cfg.Conn = types.StringValue(raw)
	state = buildStateModel(ctx, &cfg, &cfg, types.StringValue("id"), nil, 022, nil, nil)
	if state.Conn.ValueString() != raw {
		t.Fatalf("state conn = %q, want the raw override", state.Conn.ValueString())
	}
}

func TestDialResourceRequiresCompleteProviderOrDefaults(t *testing.T) {
	if _, diags := dialResource(context.Background(), nil, types.StringNull()); !diags.HasError() {
		t.Fatal("expected a diagnostic when the provider defaults are unavailable")
	}
	_, diags := dialResource(context.Background(), &providerDefaults{}, types.StringValue(`{"host":"h","user":"u"}`))
	if !diags.HasError() {
		t.Fatal("expected a diagnostic for an incomplete connection")
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Detail(), "private_key is required") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics do not explain the missing private_key: %v", diags)
	}
}
