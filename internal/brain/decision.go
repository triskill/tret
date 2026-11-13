package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Message is one turn of the conversation sent to the model.
type Message struct {
	Role    string // "system" | "user" | "assistant"
	Content string
}

// ToolCall is a single function invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON object
}

// Response is one model completion.
type Response struct {
	Content   string
	ToolCalls []ToolCall
}

// LLMProvider is one chat-completion call against a model. The bash tool
// (D7) is always offered; see tools.go.
type LLMProvider interface {
	Complete(ctx context.Context, messages []Message) (*Response, error)
}

// Decision is the deterministic routing decision derived from a Response
// (D14: route vocabulary {tool, message, finish, fail} — native tool_calls
// or a well-formed JSON route object only, never substring matching).
type Decision struct {
	Route   string // "tool" | "message" | "finish" | "fail"
	Command string // route == tool
	Text    string // route == message | finish
	Reason  string // route == fail
}

// Classify maps a model Response to a Decision.
func Classify(r *Response) Decision {
	if len(r.ToolCalls) > 1 {
		return Decision{Route: "fail", Reason: "multiple tool calls are not supported; request one bash command"}
	}
	if len(r.ToolCalls) > 0 {
		tc := r.ToolCalls[0]
		if tc.Name != BashToolName {
			return Decision{Route: "fail", Reason: fmt.Sprintf("unknown tool %q", tc.Name)}
		}
		var args struct {
			Command string `json:"command"`
		}
		err := json.Unmarshal([]byte(tc.Arguments), &args)
		if err != nil || strings.TrimSpace(args.Command) == "" {
			if err == nil {
				err = fmt.Errorf("missing command")
			}
			return Decision{Route: "fail", Reason: fmt.Sprintf("invalid bash arguments: %v", err)}
		}
		return Decision{Route: "tool", Command: args.Command}
	}
	content := strings.TrimSpace(r.Content)
	if content == "" {
		return Decision{Route: "fail", Reason: "empty model response"}
	}
	var obj struct {
		Route   string `json:"route"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal([]byte(content), &obj) == nil {
		switch obj.Route {
		case "finish":
			return Decision{Route: "finish", Text: obj.Message}
		case "fail":
			reason := obj.Error
			if reason == "" {
				reason = obj.Message
			}
			return Decision{Route: "fail", Reason: reason}
		}
	}
	return Decision{Route: "message", Text: content}
}
