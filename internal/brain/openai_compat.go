package brain

import (
	"context"
	"net/http"

	"github.com/triskill/tret/pkg/llm"
)

// OpenAICompat preserves the bundled agent's bash-enabled completion API.
// The shared transport lives in pkg/llm; generic workers do not offer bash.
type OpenAICompat struct {
	BaseURL string
	Model   string
	APIKey  string
	Client  *http.Client
}

func NewOpenAICompat(baseURL, model, apiKey string) *OpenAICompat {
	return &OpenAICompat{BaseURL: baseURL, Model: model, APIKey: apiKey, Client: llm.NewClient().HTTPClient}
}

func (o *OpenAICompat) Complete(ctx context.Context, messages []Message) (*Response, error) {
	native := make([]llm.Message, len(messages))
	for i, m := range messages {
		native[i] = llm.Message{"role": m.Role, "content": m.Content}
	}
	client := &llm.Client{HTTPClient: o.Client}
	result, err := client.Complete(ctx, llm.Profile{BaseURL: o.BaseURL, Model: o.Model, APIKey: o.APIKey}, llm.Request{Messages: native, Tools: []map[string]any{bashToolDef}})
	if err != nil {
		return nil, err
	}
	out := &Response{Content: result.Content}
	for _, tc := range result.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return out, nil
}
