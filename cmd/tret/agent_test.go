package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/triskill/tret/internal/zen"
)

func TestReportAgentResult(t *testing.T) {
	for _, tt := range []struct {
		name, state, outcome, message string
		done, failure, success        bool
	}{
		{"running", "active", "", "", false, false, false},
		{"success", "completed", "success", "", true, false, true},
		{"failure end", "completed", "failure", "tool failed", true, true, false},
		{"failure without message", "completed", "failure", "", true, true, false},
		{"completed error", "completed", "", "decide failed", true, true, false},
		{"error overrides success", "completed", "success", "decide failed", true, true, false},
		{"terminated", "terminated", "success", "", true, true, false},
		{"failed", "failed", "", "engine failed", true, true, false},
		{"old completed run", "completed", "", "", true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			done, err := reportAgentResult(&out, &zen.Instance{State: tt.state, Variables: map[string]any{
				"run_outcome": tt.outcome, "error_message": tt.message, "assistant_text": "summary",
			}})
			if done != tt.done || (err != nil) != tt.failure {
				t.Fatalf("done=%v err=%v", done, err)
			}
			if tt.message != "" && (err == nil || !strings.Contains(err.Error(), tt.message)) {
				t.Fatalf("error message lost: %v", err)
			}
			if strings.Contains(out.String(), "run outcome: success") != tt.success {
				t.Fatalf("unexpected success display: %s", &out)
			}
			if done && !strings.Contains(out.String(), "Assistant: summary") {
				t.Fatalf("missing final summary: %s", &out)
			}
		})
	}
}

func TestExecuteAgentMultipleReplies(t *testing.T) {
	var completed []int64
	cancelled := false
	jobPolls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/process-definitions":
			fmt.Fprint(w, `{"items":[{"key":3,"bpmnProcessId":"agent_loop"}]}`)
		case "/v1/process-instances":
			var body struct {
				BusinessKey string         `json:"businessKey"`
				Variables   map[string]any `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode start: %v", err)
			}
			if body.Variables["interactive"] != true || body.Variables["business_key"] != body.BusinessKey || body.BusinessKey == "" {
				t.Errorf("unexpected start variables: %+v", body)
			}
			fmt.Fprint(w, `{"key":1}`)
		case "/v1/process-instances/1":
			fmt.Fprint(w, `{"key":1,"state":"active"}`)
		case "/v1/process-instances/1/incidents":
			fmt.Fprint(w, `{"items":[]}`)
		case "/v1/process-instances/1/jobs":
			jobPolls++
			// Completed jobs deliberately remain active in subsequent responses.
			jobs := []zen.Job{{Key: 2, State: "active", Type: "agent-reply"}}
			if len(completed) >= 1 {
				jobs = append(jobs, zen.Job{Key: 3, State: "active", Type: "agent-reply", InputVariables: map[string]any{"assistant_text": "first answer"}})
			}
			if len(completed) >= 2 {
				jobs = append(jobs, zen.Job{Key: 4, State: "active", Type: "agent-reply", InputVariables: map[string]any{"assistant_text": "second answer"}})
			}
			json.NewEncoder(w).Encode(map[string]any{"items": jobs})
		case "/v1/jobs/2/complete", "/v1/jobs/3/complete":
			var key int64
			fmt.Sscanf(r.URL.Path, "/v1/jobs/%d/complete", &key)
			var body struct {
				Variables map[string]any `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode completion: %v", err)
			}
			want := "initial goal"
			if key == 3 {
				want = "follow-up"
			}
			if body.Variables["user_message"] != want {
				t.Errorf("job %d message = %v, want %q", key, body.Variables["user_message"], want)
			}
			completed = append(completed, key)
			fmt.Fprint(w, `{}`)
		case "/v1/process-instances/1/cancel":
			cancelled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	err := executeAgent([]string{"-engine", srv.URL, "-goal", "initial goal"}, strings.NewReader("follow-up\n/exit\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(completed) != "[2 3]" || !cancelled || jobPolls != 3 {
		t.Fatalf("completed=%v cancelled=%v job polls=%d; output: %s", completed, cancelled, jobPolls, &out)
	}
	if strings.Count(out.String(), "You> ") != 3 {
		t.Errorf("want one initial goal and two input prompts; output: %s", &out)
	}
	for _, text := range []string{"You> initial goal", "Assistant: first answer", "Assistant: second answer", "bye"} {
		if strings.Count(out.String(), text) != 1 {
			t.Errorf("want %q exactly once; output: %s", text, &out)
		}
	}
}

func TestExecuteAgentFailures(t *testing.T) {
	for _, tt := range []struct {
		name, failingPath, input, incidents, want string
	}{
		{name: "instance API", failingPath: "/v1/process-instances/1", want: "poll instance 1"},
		{name: "incidents API", failingPath: "/v1/process-instances/1/incidents", want: "poll incidents"},
		{name: "jobs API", failingPath: "/v1/process-instances/1/jobs", want: "poll jobs"},
		{name: "unresolved incident", incidents: `{"items":[{"key":7,"state":"unresolved","message":"bad expression"}]}`, want: "incident 7: bad expression"},
		{name: "complete API", failingPath: "/v1/jobs/2/complete", input: "hello\n", want: "complete reply job 2"},
		{name: "cancel API", failingPath: "/v1/process-instances/1/cancel", input: "/exit\n", want: "cancel instance 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tt.failingPath {
					http.Error(w, "engine unavailable", http.StatusServiceUnavailable)
					return
				}
				switch r.URL.Path {
				case "/v1/process-definitions":
					fmt.Fprint(w, `{"items":[{"key":3,"bpmnProcessId":"agent_loop"}]}`)
				case "/v1/process-instances":
					fmt.Fprint(w, `{"key":1}`)
				case "/v1/process-instances/1":
					fmt.Fprint(w, `{"key":1,"state":"active"}`)
				case "/v1/process-instances/1/incidents":
					if tt.incidents != "" {
						fmt.Fprint(w, tt.incidents)
					} else {
						fmt.Fprint(w, `{"items":[{"key":6,"state":"resolved","message":"old incident"}]}`)
					}
				case "/v1/process-instances/1/jobs":
					fmt.Fprint(w, `{"items":[{"key":2,"state":"active","type":"agent-reply"}]}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			var out bytes.Buffer
			err := executeAgent([]string{"-engine", srv.URL}, strings.NewReader(tt.input), &out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v; output: %s", tt.want, err, &out)
			}
		})
	}
}
