package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message preserves native message fields, including tool_calls and tool_call_id.
type Message map[string]any

type Request struct {
	ModelProfile string           `json:"model_profile"`
	Messages     []Message        `json:"messages"`
	Tools        []map[string]any `json:"tools,omitempty"`
	OutputSchema json.RawMessage  `json:"output_schema,omitempty"`
	MaxTokens    *int             `json:"max_tokens,omitempty"`
	Temperature  *float64         `json:"temperature,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Result struct {
	Content          string         `json:"content"`
	StructuredOutput any            `json:"structured_output,omitempty"`
	ToolCalls        []ToolCall     `json:"tool_calls"`
	FinishReason     string         `json:"finish_reason"`
	Usage            map[string]any `json:"usage"`
	Model            string         `json:"model"`
	RequestID        string         `json:"request_id"`
}

// Client makes exactly one non-streaming OpenAI-compatible completion call.
// BaseURL in the profile includes the API prefix (usually /v1).
type Client struct{ HTTPClient *http.Client }

func NewClient() *Client { return &Client{HTTPClient: &http.Client{Timeout: 120 * time.Second}} }

const maxResponseBytes = 8 << 20

func (c *Client) Complete(ctx context.Context, profile Profile, input Request) (*Result, error) {
	body := map[string]any{"model": profile.Model, "messages": input.Messages, "stream": false}
	if len(input.Tools) > 0 {
		body["tools"] = input.Tools
	}
	if input.MaxTokens != nil {
		body["max_tokens"] = *input.MaxTokens
	}
	if input.Temperature != nil {
		body["temperature"] = *input.Temperature
	}
	if len(input.OutputSchema) > 0 {
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "output", "strict": true, "schema": input.OutputSchema}}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(profile.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if profile.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+profile.APIKey)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("completion request: %w", err)
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read completion: %w", err)
	}
	if len(rb) > maxResponseBytes {
		return nil, fmt.Errorf("completion response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("completion HTTP status %d", resp.StatusCode)
	}
	var decoded struct {
		ID      string         `json:"id"`
		Model   string         `json:"model"`
		Usage   map[string]any `json:"usage"`
		Choices []struct {
			Message *struct {
				Content   *string    `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rb, &decoded); err != nil {
		return nil, fmt.Errorf("decode completion: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("completion has no choices")
	}
	choice := decoded.Choices[0]
	if choice.Message == nil || (choice.Message.Content == nil && len(choice.Message.ToolCalls) == 0) {
		return nil, fmt.Errorf("completion has no message content or tool calls")
	}
	content := ""
	if choice.Message.Content != nil {
		content = *choice.Message.Content
	}
	calls := choice.Message.ToolCalls
	if calls == nil {
		calls = []ToolCall{}
	}
	requestID := resp.Header.Get("x-request-id")
	if requestID == "" {
		requestID = decoded.ID
	}
	return &Result{Content: content, ToolCalls: calls, FinishReason: choice.FinishReason, Usage: decoded.Usage, Model: decoded.Model, RequestID: requestID}, nil
}
