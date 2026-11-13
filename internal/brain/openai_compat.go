package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAICompat talks to any OpenAI-compatible chat-completions endpoint
// (OpenAI, OpenRouter, Ollama's /v1, ...). BaseURL includes the /v1 prefix.
type OpenAICompat struct {
	BaseURL string
	Model   string
	APIKey  string
	Client  *http.Client
}

func NewOpenAICompat(baseURL, model, apiKey string) *OpenAICompat {
	return &OpenAICompat{BaseURL: baseURL, Model: model, APIKey: apiKey, Client: &http.Client{Timeout: 120 * time.Second}}
}

func (o *OpenAICompat) Complete(ctx context.Context, messages []Message) (*Response, error) {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	ms := make([]msg, len(messages))
	for i, m := range messages {
		ms[i] = msg{Role: m.Role, Content: m.Content}
	}
	body := map[string]any{
		"model":    o.Model,
		"messages": ms,
		"tools":    []any{bashToolDef},
		"stream":   false,
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("chat completions status %d: %s", resp.StatusCode, string(rb))
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rb, &r); err != nil {
		return nil, fmt.Errorf("parse chat completions response: %w", err)
	}
	if len(r.Choices) == 0 {
		return nil, fmt.Errorf("no choices in chat completions response")
	}
	m := r.Choices[0].Message
	out := &Response{Content: m.Content}
	for _, tc := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return out, nil
}
