package brain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type OpenAICompat struct {
	BaseURL string
	Model   string
	APIKey  string
	Client  *http.Client
}

func NewOpenAICompat(baseURL, model, apiKey string) *OpenAICompat {
	return &OpenAICompat{BaseURL: baseURL, Model: model, APIKey: apiKey, Client: &http.Client{Timeout: 120 * time.Second}}
}

func (o *OpenAICompat) Complete(messages []string) (string, error) {
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	ms := make([]msg, len(messages))
	for i, m := range messages {
		ms[i] = msg{Role: "user", Content: m}
	}
	body := map[string]any{"model": o.Model, "messages": ms, "stream": false}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", o.BaseURL+"/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai status %d: %s", resp.StatusCode, string(rb))
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rb, &r); err != nil {
		return "", err
	}
	if len(r.Choices) == 0 {
		return "", fmt.Errorf("no choices")
	}
	return r.Choices[0].Message.Content, nil
}
