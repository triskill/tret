package server

import (
	"context"
	"time"
	"encoding/json"

	"github.com/pbinitiative/zenbpm-go/internal/brain"
	"github.com/pbinitiative/zenbpm-go/internal/executor"
	"github.com/pbinitiative/zenbpm-go/internal/zen"
)

type Server struct {
	LLM    brain.LLMProvider
	Runner *executor.Runner
}

func New(llm brain.LLMProvider, runner *executor.Runner) *Server {
	return &Server{LLM: llm, Runner: runner}
}

func (s *Server) Decide(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
	vars := map[string]any{}
	if job != nil && len(job.InputVariables) > 0 {
		_ = json.Unmarshal(job.InputVariables, &vars)
	}
	goal, _ := vars["goal"].(string)
	if goal == "" {
		goal = "no goal"
	}
	raw, err := s.LLM.Complete([]string{"You are an agent. Return JSON {route, command, message, assistant_text}. Goal: " + goal})
	if err != nil {
		return nil, &zen.WorkerError{ErrorCode: "DECIDE_FAILED", Err: err}
	}
	var d struct {
		Route         string `json:"route"`
		Command       string `json:"command"`
		Message       string `json:"message"`
		AssistantText string `json:"assistant_text"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, &zen.WorkerError{ErrorCode: "DECIDE_PARSE", Err: err}
	}
	if d.Route == "" {
		d.Route = "fail"
	}
	res := map[string]any{"route": d.Route}
	if d.Command != "" {
		res["command"] = d.Command
	}
	if d.Message != "" {
		res["message"] = d.Message
	}
	if d.AssistantText != "" {
		res["assistant_text"] = d.AssistantText
	}
	return res, nil
}

func (s *Server) Execute(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
	vars := map[string]any{}
	if job != nil && len(job.InputVariables) > 0 {
		_ = json.Unmarshal(job.InputVariables, &vars)
	}
	cmd, _ := vars["command"].(string)
	if cmd == "" {
		return map[string]any{"result": "", "route": "finish"}, nil
	}
	r, err := s.Runner.Run(ctx, cmd, 60*time.Second) // placeholder
	_ = r
	if err != nil {
		return nil, &zen.WorkerError{ErrorCode: "EXEC_FAILED", Err: err}
	}
	out := ""
	if r != nil {
		out = r.Stdout
	}
	return map[string]any{"result": out, "route": "tool"}, nil
}
