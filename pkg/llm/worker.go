package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

const JobType = "llm-call-v1"

// Worker serves trusted process definitions. Native tool definitions are sent
// to the provider but never executed. Endpoints and secrets remain server-owned.
type Worker struct {
	profiles map[string]Profile
	Client   *Client
}

func NewWorker(profiles map[string]Profile) *Worker {
	copy := make(map[string]Profile, len(profiles))
	for name, profile := range profiles {
		copy[name] = profile
	}
	return &Worker{profiles: copy, Client: NewClient()}
}

// Error codes are catchable BPMN errors. If the process has no matching error
// boundary, ZenBPM creates an incident. Zero retries disables scheduled retries,
// not transport redelivery.
func failure(code string, err error) (map[string]any, *zenclient.WorkerError) {
	zero := int32(0)
	return nil, &zenclient.WorkerError{Err: err, ErrorCode: code, Retries: &zero}
}

func strictDecode(raw []byte, into any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func validate(input Request) (*jsonschema.Schema, error) {
	if strings.TrimSpace(input.ModelProfile) == "" {
		return nil, fmt.Errorf("model_profile is required")
	}
	if len(input.Messages) == 0 {
		return nil, fmt.Errorf("messages must not be empty")
	}
	for i, m := range input.Messages {
		role, _ := m["role"].(string)
		switch role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return nil, fmt.Errorf("messages[%d]: invalid role", i)
		}
		content, exists := m["content"]
		if exists && content != nil {
			switch parts := content.(type) {
			case string:
			case []any:
				if len(parts) == 0 {
					return nil, fmt.Errorf("messages[%d]: empty content parts", i)
				}
				for _, rawPart := range parts {
					part, ok := rawPart.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("messages[%d]: invalid content part", i)
					}
					switch part["type"] {
					case "text":
						if _, ok := part["text"].(string); !ok {
							return nil, fmt.Errorf("messages[%d]: text part requires text", i)
						}
					case "image_url":
						image, _ := part["image_url"].(map[string]any)
						location, _ := image["url"].(string)
						if strings.TrimSpace(location) == "" {
							return nil, fmt.Errorf("messages[%d]: image_url part requires url", i)
						}
					default:
						return nil, fmt.Errorf("messages[%d]: unsupported content part type", i)
					}
				}
			default:
				return nil, fmt.Errorf("messages[%d]: invalid content", i)
			}
		}
		if rawCalls, exists := m["tool_calls"]; exists {
			calls, ok := rawCalls.([]any)
			if role != "assistant" || !ok || len(calls) == 0 {
				return nil, fmt.Errorf("messages[%d]: invalid tool_calls", i)
			}
			for _, rawCall := range calls {
				call, ok := rawCall.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("messages[%d]: invalid tool call", i)
				}
				id, _ := call["id"].(string)
				function, _ := call["function"].(map[string]any)
				name, _ := function["name"].(string)
				arguments, argumentsOK := function["arguments"].(string)
				var argumentObject map[string]any
				if id == "" || call["type"] != "function" || name == "" || !argumentsOK || json.Unmarshal([]byte(arguments), &argumentObject) != nil || argumentObject == nil {
					return nil, fmt.Errorf("messages[%d]: invalid native function call", i)
				}
			}
		}
		if role == "tool" {
			if id, ok := m["tool_call_id"].(string); !ok || id == "" {
				return nil, fmt.Errorf("messages[%d]: tool_call_id required", i)
			}
		}
		if !exists || content == nil {
			if calls, ok := m["tool_calls"].([]any); role != "assistant" || !ok || len(calls) == 0 {
				return nil, fmt.Errorf("messages[%d]: content required", i)
			}
		}
	}
	for i, tool := range input.Tools {
		function, ok := tool["function"].(map[string]any)
		name, _ := function["name"].(string)
		if tool["type"] != "function" || !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("tools[%d]: function definition required", i)
		}
		if parameters, exists := function["parameters"]; exists {
			raw, err := json.Marshal(parameters)
			if err != nil {
				return nil, fmt.Errorf("tools[%d]: invalid parameters: %w", i, err)
			}
			if _, err := compileSchema(raw); err != nil {
				return nil, fmt.Errorf("tools[%d]: invalid parameters: %w", i, err)
			}
		}
	}
	if input.MaxTokens != nil && *input.MaxTokens <= 0 {
		return nil, fmt.Errorf("max_tokens must be positive")
	}
	if input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 2) {
		return nil, fmt.Errorf("temperature must be between 0 and 2")
	}
	if len(input.OutputSchema) == 0 {
		return nil, nil
	}
	return compileSchema(input.OutputSchema)
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	var shape any
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, err
	}
	switch shape.(type) {
	case map[string]any, bool:
	default:
		return nil, fmt.Errorf("schema must be an object or boolean")
	}
	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(string) (io.ReadCloser, error) { return nil, fmt.Errorf("external schema loading is disabled") }
	if err := compiler.AddResource("urn:tret:output", bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return compiler.Compile("urn:tret:output")
}

func (w *Worker) Handle(ctx context.Context, job *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(job.GetInputVariables(), &envelope); err != nil {
		return failure("invalid_input", err)
	}
	raw, ok := envelope["llm_request"]
	if !ok {
		return failure("invalid_input", fmt.Errorf("llm_request is required"))
	}
	var input Request
	if err := strictDecode(raw, &input); err != nil {
		return failure("invalid_input", err)
	}
	schema, err := validate(input)
	if err != nil {
		return failure("invalid_input", err)
	}
	profile, ok := w.profiles[input.ModelProfile]
	if !ok {
		return failure("invalid_input", fmt.Errorf("unknown model_profile %q", input.ModelProfile))
	}
	endpoint, err := url.Parse(profile.BaseURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || strings.TrimSpace(profile.Model) == "" {
		return failure("invalid_input", fmt.Errorf("invalid server model profile %q", input.ModelProfile))
	}
	result, err := w.Client.Complete(ctx, profile, input)
	if err != nil {
		return failure("provider_error", err)
	}
	if schema != nil {
		if result.FinishReason == "length" || result.FinishReason == "content_filter" {
			return failure("invalid_output", fmt.Errorf("structured completion ended with %s", result.FinishReason))
		}
		var value any
		if err := json.Unmarshal([]byte(result.Content), &value); err != nil {
			return failure("invalid_output", fmt.Errorf("completion is not JSON: %w", err))
		}
		if err := schema.Validate(value); err != nil {
			return failure("invalid_output", fmt.Errorf("completion does not match output_schema: %w", err))
		}
		result.StructuredOutput = value
	}
	// Return JSON-native variables, not Go structs, for both direct callers and
	// the engine's variable encoder.
	encoded, err := json.Marshal(result)
	if err != nil {
		return failure("invalid_output", err)
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return failure("invalid_output", err)
	}
	if schema != nil {
		value["structured_output"] = result.StructuredOutput
	}
	return map[string]any{"llm_result": value}, nil
}
