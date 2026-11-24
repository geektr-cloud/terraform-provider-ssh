package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ function.Function = (*connectionFunction)(nil)

// connectionFunction implements provider::ssh::connection(host, user, port).
// It is deterministic and does not read provider configuration or use the
// network.
type connectionFunction struct{}

type connectionFunctionParams struct {
	Host string `json:"host"`
	User string `json:"user"`
	Port string `json:"port"`
}

func (f *connectionFunction) Metadata(_ context.Context, _ function.MetadataRequest, resp *function.MetadataResponse) {
	resp.Name = "connection"
}

func (f *connectionFunction) Definition(_ context.Context, _ function.DefinitionRequest, resp *function.DefinitionResponse) {
	resp.Definition = function.Definition{
		Summary:     "Build a Connection Override JSON object",
		Description: "Returns a JSON object with only host, user, and port. It is deterministic, performs no network access, does not read provider configuration, and never accepts key material. Use it to supply `conn` for a dynamically chosen target while inheriting all remaining defaults from the provider block.",
		Parameters: []function.Parameter{
			function.StringParameter{Name: "host", Description: "Target host name or address."},
			function.StringParameter{Name: "user", Description: "Target SSH user."},
			function.StringParameter{Name: "port", Description: "Target SSH port, as a string between 1 and 65535."},
		},
		Return: function.StringReturn{},
	}
}

func (f *connectionFunction) Run(ctx context.Context, req function.RunRequest, resp *function.RunResponse) {
	var host, user, port string
	resp.Error = function.ConcatFuncErrors(
		req.Arguments.GetArgument(ctx, 0, &host),
		req.Arguments.GetArgument(ctx, 1, &user),
		req.Arguments.GetArgument(ctx, 2, &port),
	)
	if resp.Error != nil {
		return
	}
	encoded, err := json.Marshal(connectionFunctionParams{Host: host, User: user, Port: port})
	if err != nil {
		resp.Error = function.NewFuncError("failed to encode connection override")
		return
	}
	resp.Error = resp.Result.Set(ctx, types.StringValue(string(encoded)))
}
