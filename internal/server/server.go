package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/triskill/tret/internal/brain"
	"github.com/triskill/tret/internal/contextstore"
	"github.com/triskill/tret/internal/executor"
	"github.com/triskill/tret/internal/zen"
)

// executeTimeout is under the runner's 120s hard cap and well under the
// 5-minute job lock the worker registers with.
const executeTimeout = 60 * time.Second

type Server struct {
	LLM     brain.LLMProvider
	Runner  *executor.Runner
	Context *contextstore.Store
	Limits  Limits
}

func New(llm brain.LLMProvider, runner *executor.Runner, ctxStore *contextstore.Store) *Server {
	return &Server{LLM: llm, Runner: runner, Context: ctxStore, Limits: Limits{MaxToolCalls: 24, MaxModelTurns: 12, WallClockSec: 900}}
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
	return brain.SystemPrompt
}

// Decide is the agent-decide worker (D3): appends the pending user message,
// calls the LLM with the run's transcript, classifies the response into the
// D14 route vocabulary and publishes the routing variables. It always
// returns the full variable set — including user_message cleared — so the
// process scope never carries a stale value into the next turn.
func (s *Server) Decide(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
	vars := varsOf(job)
	key := contextKey(vars, job)
	out := map[string]any{
		"route": "fail", "command": "", "assistant_text": "",
		"error_message": "", "user_message": "", "context": "", "run_outcome": "",
	}

	if um, ok := vars["user_message"].(string); ok && um != "" {
		if err := s.Context.Append(key, "user", um); err != nil {
			out["error_message"] = "save user message: " + err.Error()
			return out, nil
		}
	}
	entries, err := s.Context.LoadEntries(key)
	if err != nil {
		out["error_message"] = err.Error()
		return out, nil
	}
	msgs := make([]brain.Message, 0, len(entries)+1)
	msgs = append(msgs, brain.Message{Role: "system", Content: s.systemPrompt(job, vars)})
	for _, e := range entries {
		role := "user" // tool results and system notes arrive as user turns
		if e.Role == "assistant" {
			role = "assistant"
		}
		msgs = append(msgs, brain.Message{Role: role, Content: e.Content})
	}

	resp, err := s.LLM.Complete(ctx, msgs)
	if err != nil {
		out["error_message"] = err.Error()
		return out, nil
	}
	d := brain.Classify(resp)
	out["route"] = d.Route
	var entry string
	switch d.Route {
	case "tool":
		out["command"] = d.Command
		entry = "→ bash: " + d.Command
	case "message", "finish":
		out["assistant_text"] = d.Text
		entry = d.Text
		if d.Route == "finish" {
			out["run_outcome"] = "success"
		}
	case "fail":
		out["error_message"] = d.Reason
		entry = "failed: " + d.Reason
	}
	if err := s.Context.Append(key, "assistant", entry); err != nil {
		out["route"], out["command"], out["run_outcome"] = "fail", "", ""
		out["error_message"] = "save decision: " + err.Error()
	}
	if text, err := s.Context.Load(key); err == nil {
		out["context"] = text
	}
	return out, nil
}

// Execute is the agent-execute worker (P3): runs the command the decide turn
// requested and appends the outcome to the run's transcript as a tool
// result. BPMN checks execution_error before scheduling another decision.
// Command problems (empty command, non-zero exit, timeout, launch failure)
// reach the model as tool results; a lost transcript write stops the run.
func (s *Server) Execute(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
	vars := varsOf(job)
	key := contextKey(vars, job)
	command, _ := vars["command"].(string)

	var result string
	exitCode := -1
	if strings.TrimSpace(command) == "" {
		result = "error: empty command"
	} else if res, err := s.Runner.Run(ctx, command, executeTimeout); err != nil {
		result = "error running command: " + err.Error()
	} else {
		result = formatToolResult(command, res)
		exitCode = res.ExitCode
	}

	out := map[string]any{"command": "", "exit_code": exitCode, "context": "", "execution_error": false, "error_message": ""}
	if err := s.Context.Append(key, "user", result); err != nil {
		out["execution_error"] = true
		out["error_message"] = "execution outcome could not be saved; do not retry command: " + err.Error()
	}
	if text, err := s.Context.Load(key); err == nil {
		out["context"] = text
	}
	return out, nil
}

func formatToolResult(command string, res *executor.RunResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "$ %s\nexit: %d", command, res.ExitCode)
	if res.TimedOut {
		b.WriteString(" (timed out)")
	}
	if res.Cancelled {
		b.WriteString(" (cancelled)")
	}
	if res.Stdout != "" {
		fmt.Fprintf(&b, "\nstdout:\n%s", res.Stdout)
	}
	if res.Stderr != "" {
		fmt.Fprintf(&b, "\nstderr:\n%s", res.Stderr)
	}
	if res.Truncated {
		b.WriteString("\n(output truncated)")
	}
	return b.String()
}

// FailReport is the agent-fail-report worker: logs the failure and, when
// asked, appends it to the run's transcript.
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
	return map[string]any{"run_outcome": "failure"}, nil
}
