package brain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, status int, body string, check func(map[string]any)) *OpenAICompat {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing/incorrect auth header: %q", r.Header.Get("Authorization"))
		}
		if check != nil {
			b, _ := io.ReadAll(r.Body)
			var req map[string]any
			if err := json.Unmarshal(b, &req); err != nil {
				t.Errorf("request not JSON: %v", err)
			} else {
				check(req)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return NewOpenAICompat(srv.URL, "test-model", "test-key")
}

func TestCompleteParsesContent(t *testing.T) {
	c := serve(t, 200, `{"choices":[{"message":{"role":"assistant","content":"hello there"}}]}`, func(req map[string]any) {
		if req["model"] != "test-model" {
			t.Errorf("model = %v", req["model"])
		}
		tools, _ := req["tools"].([]any)
		if len(tools) != 1 {
			t.Errorf("expected exactly the bash tool (D7), got %v", req["tools"])
		}
		if req["stream"] != false {
			t.Errorf("stream = %v", req["stream"])
		}
	})
	resp, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello there" || len(resp.ToolCalls) != 0 {
		t.Fatalf("got %+v", resp)
	}
}

func TestCompleteParsesToolCalls(t *testing.T) {
	c := serve(t, 200, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls -la\"}"}}]}}]}`, nil)
	resp, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "list files"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %+v", resp)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "bash" || tc.Arguments != `{"command":"ls -la"}` {
		t.Fatalf("got %+v", tc)
	}
}

func TestCompleteHTTPError(t *testing.T) {
	c := serve(t, 500, `{"error":"boom"}`, nil)
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCompleteNoChoices(t *testing.T) {
	c := serve(t, 200, `{"choices":[]}`, nil)
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCompleteSendsRoles(t *testing.T) {
	c := serve(t, 200, `{"choices":[{"message":{"content":"ok"}}]}`, func(req map[string]any) {
		msgs, _ := req["messages"].([]any)
		if len(msgs) != 2 {
			t.Fatalf("messages = %v", req["messages"])
		}
		m0, _ := msgs[0].(map[string]any)
		if m0["role"] != "system" {
			t.Errorf("first role = %v", m0["role"])
		}
	})
	if _, err := c.Complete(context.Background(), []Message{
		{Role: "system", Content: "you are x"},
		{Role: "user", Content: "hi"},
	}); err != nil {
		t.Fatal(err)
	}
}
