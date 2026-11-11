package brain

import (
	"strings"
)

type LLMProvider interface {
	Complete(messages []string) (string, error)
}

type MockLLM struct{}

func (m *MockLLM) Complete(messages []string) (string, error) {
	lastMsg := messages[len(messages)-1]
	
	if strings.Contains(strings.ToLower(lastMsg), "ls") || strings.Contains(strings.ToLower(lastMsg), "dir") {
		return `{"route": "tool", "command": "ls"}`, nil
	}
	
	return `{"route": "message", "content": "I am a Go agent."}`, nil
}

func ParseDecision(raw string) (string, string, string) {
	if strings.Contains(raw, `"route": "tool"`) {
		cmd := ""
		if idx := strings.Index(raw, `"command": "`); idx != -1 {
			start := idx + 11
			end := strings.Index(raw[start:], `"`)
			if end != -1 {
				cmd = raw[start : start+end]
			}
		}
		return "tool", cmd, raw
	}
	if strings.Contains(raw, `"route": "finish"`) {
		return "finish", "", raw
	}
	return "message", "", raw
}
