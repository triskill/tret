package brain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OllamaRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type OllamaResponse struct {
	Message ChatMessage `json:"message"`
	Done    bool        `json:"done"`
}

type OllamaLLM struct {
	Endpoint string
	Model    string
	Client   *http.Client
}

func NewOllamaLLM(endpoint, model string) *OllamaLLM {
	return &OllamaLLM{
		Endpoint: endpoint,
		Model:    model,
		Client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (o *OllamaLLM) Complete(messages []string) (string, error) {
	chatMsgs := make([]ChatMessage, len(messages))
	for i, m := range messages {
		parts := strings.SplitN(m, ": ", 2)
		role := "user"
		content := m
		if len(parts) == 2 {
			role = parts[0]
			content = parts[1]
		}
		chatMsgs[i] = ChatMessage{Role: role, Content: content}
	}

	reqBody := OllamaRequest{
		Model:    o.Model,
		Messages: chatMsgs,
		Stream:   false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := o.Client.Post(o.Endpoint+"/api/chat", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned status: %d", resp.StatusCode)
	}

	var ollamaResp OllamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return "", fmt.Errorf("failed to decode ollama response: %w", err)
	}

	return ollamaResp.Message.Content, nil
}
