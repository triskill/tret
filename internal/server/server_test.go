package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/triskill/tret/internal/brain"
	"github.com/triskill/tret/internal/contextstore"
	"github.com/triskill/tret/internal/executor"
	"github.com/triskill/tret/internal/zen"
)

type fakeLLM struct {
	resp   *brain.Response
	err    error
	called [][]brain.Message
}

func (f *fakeLLM) Complete(ctx context.Context, msgs []brain.Message) (*brain.Response, error) {
	f.called = append(f.called, msgs)
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func newTestServer(t *testing.T, llm brain.LLMProvider) *Server {
	t.Helper()
	runner, err := executor.NewRunner(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(llm, runner, contextstore.New(t.TempDir()))
}

func jobWithVars(vars string) *zen.WaitingJob {
	return &zen.WaitingJob{InputVariables: []byte(vars)}
}

func TestDecideMessageRoute(t *testing.T) {
	llm := &fakeLLM{resp: &brain.Response{Content: "hello human"}}
	s := newTestServer(t, llm)
	out, werr := s.Decide(context.Background(), jobWithVars(`{"business_key":"b1","user_message":"hi"}`))
	if werr != nil {
		t.Fatalf("worker error: %+v", werr)
	}
	if out["route"] != "message" || out["assistant_text"] != "hello human" {
		t.Fatalf("got %v", out)
	}
	for _, k := range []string{"command", "error_message", "user_message"} {
		if out[k] != "" {
			t.Fatalf("%s should be cleared, got %v", k, out)
		}
	}
	// system prompt is the tret workspace contract by default
	if llm.called[0][0].Role != "system" || llm.called[0][0].Content != brain.SystemPrompt {
		t.Fatalf("first message: %+v", llm.called[0][0])
	}
	// conversation reached the model and the store
	entries, _ := s.Context.LoadEntries("b1")
	if len(entries) != 2 || entries[0].Content != "hi" || entries[1].Content != "hello human" {
		t.Fatalf("store: %+v", entries)
	}
}

func TestDecideToolRoute(t *testing.T) {
	llm := &fakeLLM{resp: &brain.Response{ToolCalls: []brain.ToolCall{{ID: "1", Name: "bash", Arguments: `{"command":"pwd"}`}}}}
	s := newTestServer(t, llm)
	out, _ := s.Decide(context.Background(), jobWithVars(`{"business_key":"b2","user_message":"where am I"}`))
	if out["route"] != "tool" || out["command"] != "pwd" {
		t.Fatalf("got %v", out)
	}
	entries, _ := s.Context.LoadEntries("b2")
	if len(entries) != 2 || !strings.Contains(entries[1].Content, "pwd") {
		t.Fatalf("store: %+v", entries)
	}
}

func TestDecideFinishRoute(t *testing.T) {
	llm := &fakeLLM{resp: &brain.Response{Content: `{"route":"finish","message":"all done"}`}}
	s := newTestServer(t, llm)
	out, _ := s.Decide(context.Background(), jobWithVars(`{"business_key":"b3"}`))
	if out["route"] != "finish" || out["assistant_text"] != "all done" {
		t.Fatalf("got %v", out)
	}
}

func TestDecideFailRoute(t *testing.T) {
	llm := &fakeLLM{resp: &brain.Response{Content: `{"route":"fail","error":"hopeless"}`}}
	s := newTestServer(t, llm)
	out, _ := s.Decide(context.Background(), jobWithVars(`{"business_key":"b4"}`))
	if out["route"] != "fail" || out["error_message"] != "hopeless" {
		t.Fatalf("got %v", out)
	}
}

func TestDecideLLMErrorRoutesFail(t *testing.T) {
	llm := &fakeLLM{err: errors.New("connection refused")}
	s := newTestServer(t, llm)
	out, werr := s.Decide(context.Background(), jobWithVars(`{"business_key":"b5"}`))
	if werr != nil {
		t.Fatalf("worker error: %+v", werr)
	}
	if out["route"] != "fail" || !strings.Contains(out["error_message"].(string), "connection refused") {
		t.Fatalf("got %v", out)
	}
}

func TestDecideDoesNotReappendStaleUserMessage(t *testing.T) {
	llm := &fakeLLM{resp: &brain.Response{Content: "next"}}
	s := newTestServer(t, llm)
	if err := s.Context.Append("b6", "user", "original"); err != nil {
		t.Fatal(err)
	}
	// second decide of a tool loop: no user_message in job vars
	if _, werr := s.Decide(context.Background(), jobWithVars(`{"business_key":"b6"}`)); werr != nil {
		t.Fatalf("worker error: %+v", werr)
	}
	entries, _ := s.Context.LoadEntries("b6")
	if len(entries) != 2 || entries[0].Content != "original" || entries[1].Content != "next" {
		t.Fatalf("store: %+v", entries)
	}
}

func TestExecuteRunsCommand(t *testing.T) {
	s := newTestServer(t, &fakeLLM{})
	out, werr := s.Execute(context.Background(), jobWithVars(`{"business_key":"e1","command":"printf hello"}`))
	if werr != nil {
		t.Fatalf("worker error: %+v", werr)
	}
	if _, hasRoute := out["route"]; hasRoute {
		t.Fatalf("execute must not set route, got %v", out)
	}
	if out["exit_code"] != 0 {
		t.Fatalf("exit_code: %v", out)
	}
	ctxText, _ := out["context"].(string)
	if !strings.Contains(ctxText, "hello") || !strings.Contains(ctxText, "$ printf hello") {
		t.Fatalf("context: %q", ctxText)
	}
}

func TestExecuteFailureIsToolResultNotFailRoute(t *testing.T) {
	s := newTestServer(t, &fakeLLM{})
	out, _ := s.Execute(context.Background(), jobWithVars(`{"business_key":"e2","command":"exit 3"}`))
	if _, hasRoute := out["route"]; hasRoute {
		t.Fatalf("execute must not set route, got %v", out)
	}
	if out["exit_code"] != 3 {
		t.Fatalf("exit_code: %v", out)
	}
	if !strings.Contains(out["context"].(string), "exit: 3") {
		t.Fatalf("context: %q", out["context"])
	}
}

func TestExecuteEmptyCommandIsToolResult(t *testing.T) {
	s := newTestServer(t, &fakeLLM{})
	out, _ := s.Execute(context.Background(), jobWithVars(`{"business_key":"e3","command":""}`))
	if _, hasRoute := out["route"]; hasRoute {
		t.Fatalf("execute must not set route, got %v", out)
	}
	if !strings.Contains(out["context"].(string), "error") {
		t.Fatalf("context: %q", out["context"])
	}
}

func TestStoreFailureStopsDecisionAndExecutionLoop(t *testing.T) {
	llm := &fakeLLM{resp: &brain.Response{Content: "hello"}}
	s := newTestServer(t, llm)
	bad := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(bad, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Context = contextstore.New(bad)
	out, _ := s.Decide(context.Background(), jobWithVars(`{"user_message":"hi"}`))
	if out["route"] != "fail" || len(llm.called) != 0 {
		t.Fatalf("%v", out)
	}
	out, _ = s.Execute(context.Background(), jobWithVars(`{"command":"printf hello"}`))
	if out["execution_error"] != true || !strings.Contains(out["error_message"].(string), "do not retry") {
		t.Fatalf("%v", out)
	}
}

func TestExplicitRunOutcomes(t *testing.T) {
	s := newTestServer(t, &fakeLLM{resp: &brain.Response{Content: `{"route":"finish","message":"done"}`}})
	out, _ := s.Decide(context.Background(), jobWithVars(`{}`))
	if out["run_outcome"] != "success" {
		t.Fatalf("%v", out)
	}
	out, _ = s.FailReport(context.Background(), jobWithVars(`{"error_message":"failed"}`))
	if out["run_outcome"] != "failure" {
		t.Fatalf("%v", out)
	}
}
