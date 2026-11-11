package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pbinitiative/zenbpm-go/internal/brain"
	"github.com/pbinitiative/zenbpm-go/internal/contextstore"
	"github.com/pbinitiative/zenbpm-go/internal/executor"
	"github.com/pbinitiative/zenbpm-go/internal/zen"
)

type Server struct {
	LLM     brain.LLMProvider
	Runner  *executor.Runner
	Context *contextstore.Store
}

func New(llm brain.LLMProvider, runner *executor.Runner, ctxStore *contextstore.Store) *Server {
	return &Server{LLM: llm, Runner: runner, Context: ctxStore}
}

func varsOf(job *zen.WaitingJob) map[string]any {
	v := map[string]any{}
	if job != nil && len(job.InputVariables) > 0 {
		_ = json.Unmarshal(job.InputVariables, &v)
	}
	return v
}

func contextKey(vars map[string]any, job *zen.WaitingJob) string {
	if bk, ok := vars["business_key"].(string); ok && bk != "" {
		return bk
	}
	if job != nil && job.InstanceKey != nil {
		return fmt.Sprintf("instance-%d", *job.InstanceKey)
	}
	return "default"
}

func (s *Server) systemPrompt(job *zen.WaitingJob, vars map[string]any) string {
	if sp, ok := vars["system_prompt"].(string); ok && sp != "" {
		return sp
	}
	if job != nil && job.Headers != nil {
		if sp := job.Headers["system_prompt"]; sp != "" {
			return sp
		}
	}
	return "You are a helpful assistant."
}

func (s *Server) Decide(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
	vars := varsOf(job)
	key := contextKey(vars, job)
	ctxText, _ := s.Context.Load(key)
	if c, ok := vars["context"].(string); ok && c != "" {
		if ctxText != "" {
			ctxText = ctxText + "\n" + c
		} else {
			ctxText = c
		}
	}
	if um, ok := vars["user_message"].(string); ok && um != "" {
		_ = s.Context.Append(key, "user", um)
		if ctxText != "" {
			ctxText += "\n"
		}
		ctxText += "user: " + um
	}
	sp := s.systemPrompt(job, vars)
	raw, err := s.LLM.Complete([]string{"system: " + sp, "user: Conversation:\n" + ctxText + "\n\nReply to the latest user message."})
	if err != nil {
		return map[string]any{"route": "fail", "error_message": err.Error()}, nil
	}
	// extract assistant text (raw or JSON)
	assistant := strings.TrimSpace(raw)
	var d struct {
		AssistantText string `json:"assistant_text"`
		Message       string `json:"message"`
	}
	if json.Unmarshal([]byte(raw), &d) == nil {
		if d.AssistantText != "" {
			assistant = d.AssistantText
		} else if d.Message != "" {
			assistant = d.Message
		}
	}
	_ = s.Context.Append(key, "assistant", assistant)
	if ctxText != "" {
		ctxText += "\n"
	}
	ctxText += "assistant: " + assistant
	return map[string]any{
		"assistant_text": assistant,
		"context":        ctxText,
	}, nil
}

func (s *Server) FailReport(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
	vars := varsOf(job)
	msg, _ := vars["error_message"].(string)
	if msg == "" {
		msg = "unknown failure"
	}
	fmt.Println("FAILURE:", msg)
	key := contextKey(vars, job)
	pass := false
	if v, ok := job.Headers["pass_error_message_to_context"]; ok && (v == "true" || v == "True") {
		pass = true
	}
	if pv, ok := vars["pass_error_message_to_context"].(bool); ok {
		pass = pv
	}
	if pass {
		_ = s.Context.Append(key, "system", "ERROR: "+msg)
	}
	return map[string]any{}, nil
}
