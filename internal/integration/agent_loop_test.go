package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tret "github.com/triskill/tret"
	"github.com/triskill/tret/internal/brain"
	"github.com/triskill/tret/internal/contextstore"
	"github.com/triskill/tret/internal/executor"
	"github.com/triskill/tret/internal/server"
	"github.com/triskill/tret/internal/zen"
)

// These tests need a dedicated ZenBPM engine, with no other agent-* workers.
// Run: docker compose up -d engine
//
//	TRET_INTEGRATION=1 go test ./internal/integration -v -count=1
//
// TRET_INTEGRATION_REST and TRET_INTEGRATION_GRPC override localhost ports.
func TestAgentLoopLive(t *testing.T) {
	if os.Getenv("TRET_INTEGRATION") != "1" {
		t.Skip("set TRET_INTEGRATION=1 to use a dedicated live ZenBPM engine")
	}
	rest, addr := os.Getenv("TRET_INTEGRATION_REST"), os.Getenv("TRET_INTEGRATION_GRPC")
	if rest == "" {
		rest = "http://localhost:8080"
	}
	if addr == "" {
		addr = "localhost:9090"
	}
	client := zen.NewClient(strings.TrimRight(rest, "/"))
	client.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	definition, _, err := client.Deploy(tret.AgentLoopBPMN)
	if err != nil {
		t.Fatalf("deploy embedded BPMN (start dedicated engine first): %v", err)
	}
	conn, err := zen.DialGRPC(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.RWMutex
	servers := map[string]*server.Server{}
	worker := func(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
		var vars map[string]any
		if err := json.Unmarshal(job.InputVariables, &vars); err != nil {
			return nil, &zen.WorkerError{Err: err}
		}
		key, _ := vars["business_key"].(string)
		mu.RLock()
		srv := servers[key]
		mu.RUnlock()
		if srv == nil {
			return nil, &zen.WorkerError{Err: fmt.Errorf("unexpected run %q; use a dedicated engine", key)}
		}
		switch job.GetType() {
		case "agent-budget":
			return srv.CheckBudget(ctx, job)
		case "agent-decide":
			return srv.Decide(ctx, job)
		case "agent-execute":
			if vars["break_execution_evidence"] == true {
				// Decide has persisted its tool request. Block only the next
				// transcript write, without changing production handlers.
				dir := filepath.Join(srv.Context.Dir, "contexts")
				if err := os.RemoveAll(dir); err != nil {
					return nil, &zen.WorkerError{Err: err}
				}
				if err := os.WriteFile(dir, []byte("blocked"), 0600); err != nil {
					return nil, &zen.WorkerError{Err: err}
				}
			}
			return srv.Execute(ctx, job)
		case "agent-fail-report":
			return srv.FailReport(ctx, job)
		default:
			return nil, &zen.WorkerError{Err: fmt.Errorf("unexpected job type %s", job.GetType())}
		}
	}
	if err := zen.Register(ctx, zen.NewGrpcClient(conn), fmt.Sprintf("tret-integration-%d", time.Now().UnixNano()), worker,
		"agent-budget", "agent-decide", "agent-execute", "agent-fail-report"); err != nil {
		t.Fatal(err)
	}
	tool := brain.Response{ToolCalls: []brain.ToolCall{{ID: "call-1", Name: brain.BashToolName, Arguments: `{"command":"printf integration-tool"}`}}}
	finish := brain.Response{Content: `{"route":"finish","message":"all done"}`}
	defaults := server.Limits{MaxModelTurns: 8, MaxToolCalls: 8, WallClockSec: 60}
	cases := []struct {
		name               string
		responses          []brain.Response
		replies            []string
		limits             server.Limits
		initial            map[string]any
		outcome, errorText string
		turns, tools       int
		transcript         []string
	}{
		{name: "message_reply_finish", responses: []brain.Response{{Content: "Please reply"}, finish}, replies: []string{"first input", "second input"}, limits: defaults, outcome: "success", turns: 2, transcript: []string{"first input", "Please reply", "second input", "all done"}},
		{name: "tool_then_decide_finish", responses: []brain.Response{tool, finish}, replies: []string{"run tool"}, limits: defaults, outcome: "success", turns: 2, tools: 1, transcript: []string{"stdout:\nintegration-tool", "all done"}},
		{name: "execution_evidence_failure", responses: []brain.Response{tool}, replies: []string{"run tool"}, limits: defaults, initial: map[string]any{"break_execution_evidence": true}, outcome: "failure", errorText: "execution outcome could not be saved; do not retry command", turns: 1, tools: 1},
		{name: "explicit_failure", responses: []brain.Response{{Content: `{"route":"fail","error":"deliberate failure"}`}}, replies: []string{"fail"}, limits: defaults, outcome: "failure", errorText: "deliberate failure", turns: 1, transcript: []string{"ERROR: deliberate failure"}},
		{name: "model_budget", responses: []brain.Response{tool}, replies: []string{"loop"}, limits: server.Limits{MaxModelTurns: 1, MaxToolCalls: 8, WallClockSec: 60}, outcome: "failure", errorText: "model-turn budget exhausted", turns: 1, tools: 1},
		{name: "tool_budget", responses: []brain.Response{tool, tool}, replies: []string{"loop"}, limits: server.Limits{MaxModelTurns: 8, MaxToolCalls: 1, WallClockSec: 60}, outcome: "failure", errorText: "tool-call budget exhausted", turns: 2, tools: 1},
		{name: "wall_clock_budget", replies: []string{"too late"}, limits: defaults, initial: map[string]any{"run_started_at": time.Now().Add(-time.Hour).Unix()}, outcome: "failure", errorText: "wall-clock budget exhausted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bk := fmt.Sprintf("integration-%s-%d", tc.name, time.Now().UnixNano())
			store := contextstore.New(t.TempDir())
			runner, err := executor.NewRunner(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			llm := &scriptLLM{responses: tc.responses}
			srv := server.New(llm, runner, store)
			srv.Limits = tc.limits
			mu.Lock()
			servers[bk] = srv
			mu.Unlock()
			vars := map[string]any{"business_key": bk}
			for k, v := range tc.initial {
				vars[k] = v
			}
			key, err := client.StartInstance(definition, bk, vars)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.CancelInstance(key) })
			var previousReply int64
			for i, reply := range tc.replies {
				job := awaitReply(t, client, key, previousReply)
				if i > 0 && job.InputVariables["assistant_text"] != "Please reply" {
					t.Fatalf("reply did not receive assistant text: %#v", job.InputVariables)
				}
				previousReply = job.Key
				if err := client.CompleteJob(job.Key, map[string]any{"user_message": reply}); err != nil {
					t.Fatal(err)
				}
			}
			inst := awaitCompleted(t, client, key)
			if inst.Variables["run_outcome"] != tc.outcome {
				t.Fatalf("run_outcome=%v, want %s; vars=%#v", inst.Variables["run_outcome"], tc.outcome, inst.Variables)
			}
			assertNumber(t, inst.Variables, "model_turns", tc.turns)
			assertNumber(t, inst.Variables, "tool_calls", tc.tools)
			if started, ok := inst.Variables["run_started_at"].(float64); !ok || started <= 0 {
				t.Errorf("missing run_started_at: %#v", inst.Variables)
			}
			if tc.errorText != "" && !strings.Contains(fmt.Sprint(inst.Variables["error_message"]), tc.errorText) {
				t.Errorf("error_message=%v, want %q", inst.Variables["error_message"], tc.errorText)
			}
			if tc.outcome == "success" && inst.Variables["assistant_text"] != "all done" {
				t.Errorf("finish text lost: %#v", inst.Variables)
			}
			if inst.Variables["user_message"] != "" && tc.turns > 0 {
				t.Errorf("stale user_message: %v", inst.Variables["user_message"])
			}
			transcript, err := store.Load(bk)
			if tc.initial["break_execution_evidence"] == true {
				if inst.Variables["execution_error"] != true {
					t.Errorf("execution_error=%v, want true", inst.Variables["execution_error"])
				}
				if err == nil {
					t.Error("expected blocked transcript store")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.transcript {
				if !strings.Contains(transcript, want) {
					t.Errorf("transcript missing %q: %s", want, transcript)
				}
			}
			llm.mu.Lock()
			calls, scriptErr := llm.calls, llm.err
			llm.mu.Unlock()
			if scriptErr != nil {
				t.Error(scriptErr)
			}
			if calls != len(tc.responses) {
				t.Errorf("LLM calls=%d, want %d", calls, len(tc.responses))
			}
		})
	}
}

type scriptLLM struct {
	mu        sync.Mutex
	responses []brain.Response
	calls     int
	err       error
}

func (s *scriptLLM) Complete(_ context.Context, _ []brain.Message) (*brain.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.calls
	s.calls++
	if index >= len(s.responses) {
		s.err = fmt.Errorf("unexpected model turn %d", s.calls)
		return nil, s.err
	}
	response := s.responses[index]
	return &response, nil
}

func assertNumber(t *testing.T, vars map[string]any, name string, want int) {
	t.Helper()
	if got, ok := vars[name].(float64); !ok || got != float64(want) {
		t.Errorf("%s=%v, want %d", name, vars[name], want)
	}
}

func awaitReply(t *testing.T, client *zen.Client, key, previous int64) zen.Job {
	t.Helper()
	var found zen.Job
	await(t, client, key, func() bool {
		jobs, err := client.ListJobs(key)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if job.Type == "agent-reply" && job.State == "active" && job.Key != previous {
				found = job
				return true
			}
		}
		return false
	})
	return found
}
func awaitCompleted(t *testing.T, client *zen.Client, key int64) *zen.Instance {
	t.Helper()
	var found *zen.Instance
	await(t, client, key, func() bool {
		inst, err := client.GetInstance(key)
		if err != nil {
			t.Fatal(err)
		}
		found = inst
		if inst.State == "failed" || inst.State == "terminated" {
			t.Fatalf("unexpected engine state: %#v", inst)
		}
		return inst.State == "completed"
	})
	return found
}
func await(t *testing.T, client *zen.Client, key int64, done func() bool) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if done() {
			return
		}
		incidents, err := client.ListIncidents(key)
		if err != nil {
			t.Fatal(err)
		}
		if len(incidents) > 0 {
			t.Fatalf("engine incidents: %#v", incidents)
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			inst, _ := client.GetInstance(key)
			jobs, _ := client.ListJobs(key)
			t.Fatalf("timed out waiting for instance %d: instance=%#v jobs=%#v", key, inst, jobs)
		}
	}
}
