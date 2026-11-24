package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/triskill/tret/internal/zen"
)

func response(action, command, answer string) map[string]any {
	structured := map[string]any{"action": action, "command": command, "answer": answer}
	raw, _ := json.Marshal(structured)
	return map[string]any{"content": string(raw), "structured_output": structured}
}

func TestConversation(t *testing.T) {
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := session{output: &output, messages: []map[string]any{{"role": "system", "content": systemPrompt}}, read: terminalReader(ctx, strings.NewReader("fix it\ny\n\ncontinue\n/exit\n"))}
	input := zen.Job{Type: "tret-chat-input"}
	vars, err := s.handle(input)
	if err != nil {
		t.Fatal(err)
	}
	if vars["turn"] != 0 || vars["quit"] != false {
		t.Fatalf("input: %v", vars)
	}
	request := vars["request"].(map[string]any)
	if request["model_profile"] != "default" || request["max_tokens"] != 2048 {
		t.Fatalf("request: %v", request)
	}
	schema := request["output_schema"].(map[string]any)
	if schema["additionalProperties"] != false || len(schema["required"].([]string)) != 3 {
		t.Fatalf("schema: %v", schema)
	}
	approve := zen.Job{Type: "tret-chat-approve", InputVariables: map[string]any{"response": response("shell", "pwd", "")}}
	vars, err = s.handle(approve)
	if err != nil || vars["approved"] != true {
		t.Fatalf("approval: %v %v", vars, err)
	}
	execution := map[string]any{"stdout": "/tmp", "stderr": "", "exit_code": 0, "timed_out": false, "cancelled": false, "truncated": false}
	_, err = s.handle(zen.Job{Type: "tret-chat-observe", InputVariables: map[string]any{"execution": execution}})
	if err != nil {
		t.Fatal(err)
	}
	last := s.messages[len(s.messages)-1]
	if last["role"] != "user" || !strings.Contains(last["content"].(string), `"stdout":"/tmp"`) {
		t.Fatalf("tool output: %v", last)
	}
	vars, err = s.handle(approve)
	if err != nil || vars["approved"] != false {
		t.Fatalf("denial: %v %v", vars, err)
	}
	if !strings.Contains(s.messages[len(s.messages)-1]["content"].(string), "denied") {
		t.Fatal("missing denial feedback")
	}
	input.InputVariables = map[string]any{"response": response("shell", "pwd", "")}
	vars, err = s.handle(input)
	if err != nil || vars["turn"] != 0 {
		t.Fatalf("budget: %v %v", vars, err)
	}
	input.InputVariables["response"] = response("answer", "", "all done")
	vars, err = s.handle(input)
	if err != nil || vars["quit"] != true {
		t.Fatalf("quit: %v %v", vars, err)
	}
	for _, want := range []string{`$ "pwd"`, "exit_code=0", "Turn budget reached", "all done"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, output.String())
		}
	}
}

func TestTerminalEscapesUntrustedOutput(t *testing.T) {
	var output bytes.Buffer
	s := session{output: &output, read: func() (string, error) { return "y", nil }}
	command := "printf 'ok'\r\x1b\u009b\u202e\n"
	proposed := response("shell", command, "")
	vars, err := s.handle(zen.Job{Type: "tret-chat-approve", InputVariables: map[string]any{"response": proposed}})
	if err != nil || vars["approved"] != true {
		t.Fatalf("approval: %v %v", vars, err)
	}
	if got := output.String(); got != `$ "printf 'ok'\r\x1b\u009b\u202e\n"`+"\nApprove? [y/N] " {
		t.Errorf("approval displayed %q", got)
	}
	if s.messages[0]["content"] != proposed["content"] {
		t.Fatal("approval display changed the transcript")
	}

	output.Reset()
	execution := map[string]any{"stdout": "café\n\x1b\r", "stderr": "\x07\u202e", "exit_code": 0, "timed_out": false, "cancelled": false, "truncated": false}
	if _, err := s.handle(zen.Job{Type: "tret-chat-observe", InputVariables: map[string]any{"execution": execution}}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "stdout:\ncafé\n\\x1b\\r\nstderr:\n\\a\\u202e\n") {
		t.Errorf("execution displayed %q", got)
	}

	output.Reset()
	if _, err := s.handle(zen.Job{Type: "tret-chat-input", InputVariables: map[string]any{"response": response("answer", "", "done\n\u009b\b")}}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "done\n\\u009b\\b\n> " {
		t.Errorf("answer displayed %q", got)
	}
}

func TestTranscriptLimit(t *testing.T) {
	s := session{}
	if err := s.append("user", strings.Repeat("x", maxMessageBytes)); err == nil {
		t.Fatal("expected transcript limit error")
	}
	if len(s.messages) != 0 {
		t.Fatal("oversize message was retained")
	}
}

func TestReaderEOFAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read := terminalReader(ctx, strings.NewReader(""))
	for i := 0; i < 2; i++ {
		if _, err := read(); !errors.Is(err, io.EOF) {
			t.Fatalf("EOF: %v", err)
		}
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx2, cancel2 := context.WithCancel(context.Background())
	read2 := terminalReader(ctx2, reader)
	result := make(chan error, 1)
	go func() { _, err := read2(); result <- err }()
	cancel2()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked input ignored cancellation")
	}
}

func TestRunFiltersAndCompletesOnce(t *testing.T) {
	polls, completions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/process-instances/9":
			state := "active"
			if polls >= 2 {
				state = "completed"
			}
			fmt.Fprintf(w, `{"key":9,"state":%q}`, state)
		case strings.HasSuffix(r.URL.Path, "/incidents"):
			io.WriteString(w, `{"items":[]}`)
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			polls++
			io.WriteString(w, `{"items":[{"key":1,"type":"tret-chat-input","state":"completed"},{"key":2,"type":"llm-call-v1","state":"active"},{"key":3,"type":"tret-chat-input","state":"active"}]}`)
		case r.URL.Path == "/v1/jobs/3/complete":
			var body struct {
				Variables map[string]any `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Variables["quit"] != true {
				t.Errorf("completion: %v", body)
			}
			completions++
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := Run(ctx, zen.NewClient(server.URL), 9, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if completions != 1 {
		t.Fatalf("completed %d times", completions)
	}
}

func TestRunIncident(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/incidents") {
			io.WriteString(w, `{"items":[{"key":4,"state":"unresolved","message":"provider failed"}]}`)
			return
		}
		io.WriteString(w, `{"key":9,"state":"active"}`)
	}))
	defer server.Close()
	err := Run(context.Background(), zen.NewClient(server.URL), 9, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "provider failed") {
		t.Fatalf("incident: %v", err)
	}
}

func TestApprovalExit(t *testing.T) {
	for _, input := range []string{"", "/exit\n"} {
		t.Run(fmt.Sprintf("input=%q", input), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := session{output: io.Discard, read: terminalReader(ctx, strings.NewReader(input))}
			_, err := s.handle(zen.Job{Type: "tret-chat-approve", InputVariables: map[string]any{"response": response("shell", "pwd", "")}})
			if !errors.Is(err, io.EOF) {
				t.Fatalf("approval exit: %v", err)
			}
		})
	}
}
