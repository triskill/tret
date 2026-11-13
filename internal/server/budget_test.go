package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestBudgetBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, phase  string
		turns, tools int
		started      int64
		allowed      bool
	}{
		{"first model", "decide", 0, 0, 0, true},
		{"last model", "decide", 1, 0, 0, true},
		{"model exhausted", "decide", 2, 0, 0, false},
		{"last tool", "execute", 2, 1, 0, true},
		{"tool exhausted", "execute", 1, 2, 0, false},
		{"deadline", "decide", 0, 0, time.Now().Unix() - 100, false},
		{"bad phase", "unknown", 0, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, &fakeLLM{})
			s.Limits = Limits{MaxModelTurns: 2, MaxToolCalls: 2, WallClockSec: 60}
			b, _ := json.Marshal(map[string]any{"model_turns": tc.turns, "tool_calls": tc.tools, "run_started_at": tc.started})
			job := jobWithVars(string(b))
			job.Headers = map[string]string{"phase": tc.phase}
			out, err := s.CheckBudget(context.Background(), job)
			if err != nil || out["budget_allowed"] != tc.allowed {
				t.Fatalf("%v %v", out, err)
			}
			turns, tools := int64(tc.turns), int64(tc.tools)
			if tc.allowed && tc.phase == "decide" {
				turns++
			}
			if tc.allowed && tc.phase == "execute" {
				tools++
			}
			if out["model_turns"] != turns || out["tool_calls"] != tools {
				t.Fatalf("bad counters: %v", out)
			}
			if !tc.allowed && out["error_message"] == "" {
				t.Fatal("missing denial reason")
			}
		})
	}
}
