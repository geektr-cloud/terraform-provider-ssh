package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestConnectionFunctionDefinition(t *testing.T) {
	fn := &connectionFunction{}
	var metadata function.MetadataResponse
	fn.Metadata(context.Background(), function.MetadataRequest{}, &metadata)
	if metadata.Name != "connection" {
		t.Fatalf("name = %q", metadata.Name)
	}

	var def function.DefinitionResponse
	fn.Definition(context.Background(), function.DefinitionRequest{}, &def)
	if def.Diagnostics.HasError() {
		t.Fatalf("definition diagnostics: %v", def.Diagnostics)
	}
	if len(def.Definition.Parameters) != 3 {
		t.Fatalf("parameters = %d, want 3", len(def.Definition.Parameters))
	}
	for i, parameter := range def.Definition.Parameters {
		if _, ok := parameter.(function.StringParameter); !ok {
			t.Fatalf("parameter %d is %T, want StringParameter", i, parameter)
		}
	}
	if _, ok := def.Definition.Return.(function.StringReturn); !ok {
		t.Fatalf("return is %T, want StringReturn", def.Definition.Return)
	}
	if def.Definition.VariadicParameter != nil {
		t.Fatal("function must not be variadic")
	}
}

func TestConnectionFunctionRun(t *testing.T) {
	fn := &connectionFunction{}
	args := function.NewArgumentsData([]attr.Value{
		types.StringValue("example.internal"),
		types.StringValue("deploy"),
		types.StringValue("2222"),
	})
	resp := &function.RunResponse{Result: function.NewResultData(types.StringUnknown())}
	fn.Run(context.Background(), function.RunRequest{Arguments: args}, resp)
	if resp.Error != nil {
		t.Fatalf("run error: %v", resp.Error)
	}
	result, ok := resp.Result.Value().(types.String)
	if !ok {
		t.Fatalf("result is %T", resp.Result.Value())
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result.ValueString()), &decoded); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if len(decoded) != 3 || decoded["host"] != "example.internal" || decoded["user"] != "deploy" || decoded["port"] != "2222" {
		t.Fatalf("decoded = %#v", decoded)
	}
	if result.ValueString() != `{"host":"example.internal","user":"deploy","port":"2222"}` {
		t.Fatalf("deterministic encoding changed: %q", result.ValueString())
	}

	// The function output must be a valid Connection Override on its own.
	if _, err := parseConnOverride(result.ValueString()); err != nil {
		t.Fatalf("function output is not a valid override: %v", err)
	}
}

func TestProviderRegistersFunction(t *testing.T) {
	p := New("test")().(provider.ProviderWithFunctions)
	functions := p.Functions(context.Background())
	if len(functions) != 1 {
		t.Fatalf("functions = %d, want 1", len(functions))
	}
	var metadata function.MetadataResponse
	functions[0]().Metadata(context.Background(), function.MetadataRequest{}, &metadata)
	if metadata.Name != "connection" {
		t.Fatalf("function name = %q", metadata.Name)
	}
}
