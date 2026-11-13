package main

import (
	"bytes"
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
