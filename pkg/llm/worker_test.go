package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
)

var _ zenclient.WorkerFunc = NewWorker(nil).Handle

func runInput(t *testing.T, w *Worker, input map[string]any) (map[string]any, *zenclient.WorkerError) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"llm_request": input})
	if err != nil {
		t.Fatal(err)
	}
	return w.Handle(context.Background(), &proto.WaitingJob{InputVariables: b})
}
func basicInput() map[string]any {
	return map[string]any{"model_profile": "default", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
}
func fixture(t *testing.T, body string, status int, check func(map[string]any)) *Worker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request path/auth = %s/%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if check != nil {
			check(request)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return NewWorker(map[string]Profile{"default": {BaseURL: srv.URL + "/v1/", Model: "server-model", APIKey: "secret"}})
}
func TestWorkerOneCallNoTools(t *testing.T) {
	calls := 0
	worker := fixture(t, `{"id":"completion-1","model":"actual","choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"total_tokens":3}}`, 200, func(req map[string]any) {
		calls++
		if _, ok := req["tools"]; ok {
			t.Error("default request offers tools")
		}
		if req["model"] != "server-model" || req["stream"] != false {
			t.Errorf("request = %#v", req)
		}
	})
	output, err := runInput(t, worker, basicInput())
	if err != nil {
		t.Fatal(err)
	}
	result := output["llm_result"].(map[string]any)
	if calls != 1 || result["content"] != "hello" || result["model"] != "actual" || result["request_id"] != "completion-1" || result["finish_reason"] != "stop" {
		t.Fatalf("calls=%d result=%#v", calls, result)
	}
	if _, ok := result["structured_output"]; ok {
		t.Fatal("unrequested structured output")
	}
	if len(result["tool_calls"].([]any)) != 0 {
		t.Fatal(result)
	}
}
func TestWorkerNativeToolsAndMessages(t *testing.T) {
	worker := fixture(t, `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"next-id","type":"function","function":{"name":"search","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, 200, func(req map[string]any) {
		messages := req["messages"].([]any)
		if messages[1].(map[string]any)["tool_call_id"] != "previous-id" {
			t.Fatal(messages)
		}
		previous := messages[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
		if previous["id"] != "previous-id" {
			t.Fatal(previous)
		}
		if len(req["tools"].([]any)) != 1 {
			t.Fatal(req)
		}
	})
	input := basicInput()
	input["messages"] = []any{map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "previous-id", "type": "function", "function": map[string]any{"name": "search", "arguments": "{}"}}}}, map[string]any{"role": "tool", "tool_call_id": "previous-id", "content": "found"}}
	input["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "search", "parameters": map[string]any{"type": "object"}}}}
	output, err := runInput(t, worker, input)
	if err != nil {
		t.Fatal(err)
	}
	call := output["llm_result"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "next-id" || call["function"].(map[string]any)["arguments"] != "{}" {
		t.Fatal(call)
	}
}
func TestWorkerSchemaValidation(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"approved": map[string]any{"type": "boolean"}}, "required": []string{"approved"}, "additionalProperties": false}
	for _, tc := range []struct{ name, content, code string }{
		{"valid", `{"approved":true}`, ""}, {"type", `{"approved":"true"}`, "invalid_output"}, {"missing", `{}`, "invalid_output"}, {"extra", `{"approved":true,"extra":1}`, "invalid_output"}, {"non-json", `not json`, "invalid_output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": tc.content}}}})
			worker := fixture(t, string(b), 200, func(req map[string]any) {
				if req["response_format"].(map[string]any)["type"] != "json_schema" {
					t.Fatal(req)
				}
			})
			input := basicInput()
			input["output_schema"] = schema
			output, err := runInput(t, worker, input)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if output["llm_result"].(map[string]any)["structured_output"].(map[string]any)["approved"] != true {
				t.Fatal(output)
			}
		})
	}
}
func assertCode(t *testing.T, err *zenclient.WorkerError, code string) {
	t.Helper()
	if err == nil || err.ErrorCode != code || err.Retries == nil || *err.Retries != 0 {
		t.Fatalf("expected %s with zero retries; got %#v", code, err)
	}
}
func TestInvalidInputBeforeNetwork(t *testing.T) {
	worker := fixture(t, `{}`, 200, func(map[string]any) { t.Error("invalid input made a network request") })
	for _, mutate := range []func(map[string]any){
		func(i map[string]any) { delete(i, "messages") }, func(i map[string]any) { i["model_profile"] = "unknown" }, func(i map[string]any) { i["base_url"] = "https://evil.invalid" }, func(i map[string]any) { i["max_tokens"] = -1 }, func(i map[string]any) { i["max_tokens"] = 1.5 }, func(i map[string]any) { i["temperature"] = 3 }, func(i map[string]any) { i["messages"] = []any{map[string]any{"role": "tool", "content": "x"}} }, func(i map[string]any) { i["tools"] = []any{map[string]any{"type": "unknown"}} }, func(i map[string]any) { i["output_schema"] = map[string]any{"type": "nonsense"} }, func(i map[string]any) { i["output_schema"] = map[string]any{"$ref": "https://example.invalid/schema"} },
	} {
		input := basicInput()
		mutate(input)
		_, err := runInput(t, worker, input)
		assertCode(t, err, "invalid_input")
	}
}
func TestProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{500, `secret provider detail`}, {200, `not json`}, {200, `{"choices":[]}`}, {200, `{"choices":[{}]}`}, {200, `{"choices":[{"message":{}}]}`}, {200, `{"choices":[{"message":{"content":null}}]}`}, {200, strings.Repeat("x", maxResponseBytes+1)}} {
		worker := fixture(t, tc.body, tc.status, nil)
		_, err := runInput(t, worker, basicInput())
		assertCode(t, err, "provider_error")
		if strings.Contains(err.Error(), "secret provider detail") {
			t.Fatal("provider body leaked")
		}
	}
}

func TestStructuredOutputRejectsIncompleteFinish(t *testing.T) {
	for _, reason := range []string{"length", "content_filter"} {
		t.Run(reason, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "{}"}, "finish_reason": reason}}})
			worker := fixture(t, string(body), 200, nil)
			input := basicInput()
			input["output_schema"] = map[string]any{"type": "object"}
			_, err := runInput(t, worker, input)
			assertCode(t, err, "invalid_output")
		})
	}
}

func TestNativeShapesValidatedBeforeNetwork(t *testing.T) {
	worker := fixture(t, `{}`, 200, func(map[string]any) { t.Error("malformed native shape reached provider") })
	for _, message := range []map[string]any{
		{"role": "user", "content": []any{7}},
		{"role": "user", "content": []any{map[string]any{"type": "text", "text": 7}}},
		{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{}}}},
		{"role": "assistant", "tool_calls": []any{7}},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "x", "type": "function", "function": map[string]any{"name": "f", "arguments": 7}}}},
		{"role": "assistant", "tool_calls": []any{map[string]any{"id": "x", "type": "function", "function": map[string]any{"name": "f", "arguments": "[]"}}}},
	} {
		input := basicInput()
		input["messages"] = []any{message}
		_, err := runInput(t, worker, input)
		assertCode(t, err, "invalid_input")
	}
	for _, parameters := range []any{7, nil, []any{}, map[string]any{"type": "bogus"}, map[string]any{"$ref": "https://example.invalid/schema"}} {
		input := basicInput()
		input["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "f", "parameters": parameters}}}
		_, err := runInput(t, worker, input)
		assertCode(t, err, "invalid_input")
	}
}

func TestNativeContentPartsAndBooleanToolSchema(t *testing.T) {
	calls := 0
	worker := fixture(t, `{"choices":[{"message":{"content":"ok"}}]}`, 200, func(request map[string]any) {
		calls++
		parts := request["messages"].([]any)[0].(map[string]any)["content"].([]any)
		if len(parts) != 2 || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,AA==" {
			t.Fatal(parts)
		}
	})
	input := basicInput()
	input["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "inspect"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA=="}}}}}
	input["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "f", "parameters": true}}}
	if _, err := runInput(t, worker, input); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

// The provider call must survive a broken job stream. The engine cancels the
// job context when the stream to the worker breaks and hands the job out
// again; a call which dies with the stream is a paid-for call whose answer
// is thrown away.
func TestCallSurvivesCancelledJobContext(t *testing.T) {
	calls := 0
	worker := fixture(t, `{"choices":[{"message":{"content":"hello"}}]}`, 200, func(map[string]any) { calls++ })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, _ := json.Marshal(map[string]any{"llm_request": basicInput()})
	output, err := worker.Handle(ctx, &proto.WaitingJob{InputVariables: b})
	if err != nil {
		t.Fatalf("cancelled job context aborted the call: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if output["llm_result"].(map[string]any)["content"] != "hello" {
		t.Fatalf("result=%v", output)
	}
}
