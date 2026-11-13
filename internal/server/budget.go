package server

import (
	"context"
	"fmt"
	"time"

	"github.com/triskill/tret/internal/zen"
)

type Limits struct {
	MaxToolCalls  int
	MaxModelTurns int
	WallClockSec  int
}

// CheckBudget performs one BPMN step. The process persists its counters and
// routes budget_allowed; this worker never dispatches another worker.
func (s *Server) CheckBudget(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
	vars := varsOf(job)
	now := time.Now().Unix()
	started := number(vars["run_started_at"])
	if started == 0 {
		started = now
	}
	turns, tools := number(vars["model_turns"]), number(vars["tool_calls"])
	out := map[string]any{"budget_allowed": false, "error_message": "", "run_started_at": started, "model_turns": turns, "tool_calls": tools}
	reason := ""
	phase := job.Headers["phase"]
	switch {
	case s.Limits.MaxModelTurns <= 0 || s.Limits.MaxToolCalls <= 0 || s.Limits.WallClockSec <= 0:
		reason = "run budgets must be positive"
	case now-started >= int64(s.Limits.WallClockSec):
		reason = "wall-clock budget exhausted"
	case phase == "decide":
		if turns >= int64(s.Limits.MaxModelTurns) {
			reason = "model-turn budget exhausted"
		} else {
			out["model_turns"] = turns + 1
		}
	case phase == "execute":
		if tools >= int64(s.Limits.MaxToolCalls) {
			reason = "tool-call budget exhausted"
		} else {
			out["tool_calls"] = tools + 1
		}
	default:
		reason = fmt.Sprintf("unknown budget phase %q", phase)
	}
	out["budget_allowed"] = reason == ""
	out["error_message"] = reason
	return out, nil
}

func number(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}
