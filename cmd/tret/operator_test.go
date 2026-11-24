package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// operatorEngine serves the answers of the engine to the operator commands
// and records what the commands asked for.
type operatorEngine struct {
	instances string
	instance  string
	jobs      string
	incidents string
	resolved  []string
	completed []string
	queries   []string
}

func (e *operatorEngine) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/process-instances" && r.Method == http.MethodGet:
			e.queries = append(e.queries, r.URL.RawQuery)
			fmt.Fprint(w, e.instances)
		case strings.HasSuffix(r.URL.Path, "/jobs") && r.Method == http.MethodGet:
			fmt.Fprint(w, e.jobs)
		case strings.HasSuffix(r.URL.Path, "/incidents") && r.Method == http.MethodGet:
			fmt.Fprint(w, e.incidents)
		case strings.HasPrefix(r.URL.Path, "/v1/incidents/") && r.Method == http.MethodPost:
			e.resolved = append(e.resolved, r.URL.Path)
			w.WriteHeader(http.StatusCreated)
		case strings.HasPrefix(r.URL.Path, "/v1/jobs/") && r.Method == http.MethodPost:
			e.completed = append(e.completed, r.URL.Path)
			w.WriteHeader(http.StatusCreated)
		case strings.HasPrefix(r.URL.Path, "/v1/process-instances/") && r.Method == http.MethodGet:
			fmt.Fprint(w, e.instance)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExecuteStatus(t *testing.T) {
	engine := &operatorEngine{instances: `{"partitions":[{"items":[
		{"key":2125313464510451712,"state":"active","bpmnProcessId":"probe_process","processDefinitionKey":7,"businessKey":"paging-3","incidentCount":2},
		{"key":2125313464283959296,"state":"active","bpmnProcessId":"probe_process","processDefinitionKey":7,"businessKey":"paging-2","incidentCount":0}
	],"partition":1,"totalCount":2}],"count":2,"page":1,"size":100,"totalCount":2}`}
	srv := engine.start(t)
	var out bytes.Buffer
	if err := executeStatus([]string{"-engine", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"KEY", "STATE", "BUSINESS KEY", "2125313464510451712", "active", "paging-3", "2125313464283959296", "paging-2"} {
		if !strings.Contains(text, want) {
			t.Errorf("the status does not mention %q:\n%s", want, text)
		}
	}
	if len(engine.queries) != 1 || !strings.Contains(engine.queries[0], "state=active") {
		t.Errorf("the status did not ask for the active instances: %v", engine.queries)
	}
}

func TestExecuteStatusWithoutInstances(t *testing.T) {
	engine := &operatorEngine{instances: `{"partitions":[],"count":0,"page":1,"size":100,"totalCount":0}`}
	srv := engine.start(t)
	var out bytes.Buffer
	if err := executeStatus([]string{"-engine", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "KEY") {
		t.Errorf("no table in the empty status:\n%s", out.String())
	}
}

func TestExecuteInstance(t *testing.T) {
	engine := &operatorEngine{instance: `{"key":2125313464510451712,"state":"active","bpmnProcessId":"probe_process","processDefinitionKey":7,"businessKey":"paging-3","incidentCount":2,"variables":{"answer":"42"}}`}
	srv := engine.start(t)
	var out bytes.Buffer
	if err := executeInstance([]string{"2125313464510451712", "-engine", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"2125313464510451712", "active", "probe_process", "paging-3", `"answer": "42"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the instance does not mention %q:\n%s", want, text)
		}
	}
}

func TestExecuteJobs(t *testing.T) {
	engine := &operatorEngine{jobs: `{"items":[{"key":2125313374932701185,"type":"probe-v1","state":"failed","elementId":"Probe","attempts":0,"retries":1}],"count":1,"page":1,"size":10,"totalCount":1}`}
	srv := engine.start(t)
	var out bytes.Buffer
	if err := executeJobs([]string{"2125313464510451712", "-engine", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"KEY", "TYPE", "STATE", "ELEMENT", "2125313374932701185", "probe-v1", "failed", "Probe"} {
		if !strings.Contains(text, want) {
			t.Errorf("the jobs do not mention %q:\n%s", want, text)
		}
	}
}

func TestExecuteIncidents(t *testing.T) {
	engine := &operatorEngine{incidents: `{"items":[
		{"key":2125313375280828416,"message":"failed to complete job","elementId":"Probe","jobKey":2125313374932701185,"processInstanceKey":2125313464510451712},
		{"key":2125313375280828417,"message":"resolved earlier","elementId":"Probe","jobKey":2125313374932701185,"resolvedAt":"2026-11-24T19:39:38.000Z"}
	],"count":2,"page":1,"size":10,"totalCount":2}`}
	srv := engine.start(t)
	var out bytes.Buffer
	if err := executeIncidents([]string{"2125313464510451712", "-engine", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"KEY", "MESSAGE", "2125313375280828416", "failed to complete job", "2125313375280828417", "resolved earlier"} {
		if !strings.Contains(text, want) {
			t.Errorf("the incidents do not mention %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "no") || !strings.Contains(text, "yes") {
		t.Errorf("the incidents do not say which are resolved:\n%s", text)
	}
}

func TestExecuteResolve(t *testing.T) {
	engine := &operatorEngine{}
	srv := engine.start(t)
	var out bytes.Buffer
	if err := executeResolve([]string{"2125313375280828416", "-engine", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if len(engine.resolved) != 1 || engine.resolved[0] != "/v1/incidents/2125313375280828416/resolve" {
		t.Fatalf("resolved=%v", engine.resolved)
	}
	if !strings.Contains(out.String(), "2125313375280828416") {
		t.Errorf("the resolve does not name the incident:\n%s", out.String())
	}
}

func TestExecuteComplete(t *testing.T) {
	engine := &operatorEngine{}
	srv := engine.start(t)
	var out bytes.Buffer
	if err := executeComplete([]string{"2125313374932701185", "-engine", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if len(engine.completed) != 1 || engine.completed[0] != "/v1/jobs/2125313374932701185/complete" {
		t.Fatalf("completed=%v", engine.completed)
	}
}

func TestExecuteVersionAndCapabilities(t *testing.T) {
	var out bytes.Buffer
	if err := executeVersion(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "tret ") {
		t.Errorf("version output: %q", out.String())
	}
	out.Reset()
	if err := executeCapabilities(nil, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"llm-call-v1", "exec-command-v1", "status", "incidents", "resolve"} {
		if !strings.Contains(text, want) {
			t.Errorf("the capabilities do not mention %q:\n%s", want, text)
		}
	}
}

func TestOperatorCommandsRejectInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		name    string
		execute func([]string, io.Writer) error
		args    []string
	}{
		{"status unknown state", executeStatus, []string{"-state", "bogus"}},
		{"status negative limit", executeStatus, []string{"-limit", "-1"}},
		{"status positional argument", executeStatus, []string{"1"}},
		{"instance without key", executeInstance, nil},
		{"instance with two keys", executeInstance, []string{"1", "2"}},
		{"instance with a bad key", executeInstance, []string{"abc"}},
		{"instance with a zero key", executeInstance, []string{"0"}},
		{"jobs without key", executeJobs, nil},
		{"incidents without key", executeIncidents, nil},
		{"resolve without key", executeResolve, nil},
		{"resolve with a bad key", executeResolve, []string{"abc"}},
		{"resolve with negative retries", executeResolve, []string{"1", "-retries", "-1"}},
		{"complete without key", executeComplete, nil},
		{"complete with a missing variables file", executeComplete, []string{"1", "-variables", "/nonexistent/vars.json"}},
		{"capabilities with a positional argument", executeCapabilities, []string{"1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tt.execute(tt.args, &out); err == nil {
				t.Fatal("expected error")
			}
			if out.Len() != 0 {
				t.Fatalf("failure printed output: %s", &out)
			}
		})
	}
}

// A flag the command does not know is refused. The usage goes to the output
// of the command, as it does for the other commands of tret.
func TestOperatorCommandsRejectUnknownFlags(t *testing.T) {
	for _, tt := range []struct {
		name    string
		execute func([]string, io.Writer) error
		args    []string
	}{
		{"status unknown flag", executeStatus, []string{"-unknown"}},
		{"instance unknown flag", executeInstance, []string{"1", "-unknown"}},
		{"instance negative key", executeInstance, []string{"-1"}},
		{"jobs unknown flag", executeJobs, []string{"-unknown"}},
		{"incidents unknown flag", executeIncidents, []string{"-unknown"}},
		{"resolve unknown flag", executeResolve, []string{"-unknown"}},
		{"complete unknown flag", executeComplete, []string{"-unknown"}},
		{"version unknown flag", executeVersion, []string{"-unknown"}},
		{"capabilities unknown flag", executeCapabilities, []string{"-unknown"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tt.execute(tt.args, &out); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// The key of a command may come before or after its flags.
func TestOperatorCommandsTakeTheKeyBeforeOrAfterTheFlags(t *testing.T) {
	engine := &operatorEngine{instance: `{"key":7,"state":"active","variables":{}}`}
	srv := engine.start(t)
	for _, args := range [][]string{
		{"7", "-engine", srv.URL},
		{"-engine", srv.URL, "7"},
		{"-engine", srv.URL, "7", "-engine", srv.URL},
	} {
		var out bytes.Buffer
		if err := executeInstance(args, &out); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "key: 7") {
			t.Fatalf("%v: %s", args, out.String())
		}
	}
}

func TestOperatorCommandHelp(t *testing.T) {
	for _, execute := range []func([]string, io.Writer) error{
		executeStatus, executeInstance, executeJobs, executeIncidents, executeResolve, executeComplete, executeVersion, executeCapabilities,
	} {
		var out bytes.Buffer
		if err := execute([]string{"-h"}, &out); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("help error: %v", err)
		}
		if out.Len() == 0 {
			t.Fatal("missing help output")
		}
	}
}

func TestOperatorCommandsReportEngineFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "engine is down")
	}))
	defer srv.Close()
	for _, tt := range []struct {
		name    string
		execute func([]string, io.Writer) error
		args    []string
		want    string
	}{
		{"status", executeStatus, nil, "engine is down"},
		{"instance", executeInstance, []string{"1"}, "engine is down"},
		{"jobs", executeJobs, []string{"1"}, "engine is down"},
		{"incidents", executeIncidents, []string{"1"}, "engine is down"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			args := append(append([]string{}, tt.args...), "-engine", srv.URL)
			err := tt.execute(args, &out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
			if out.Len() != 0 {
				t.Fatalf("failure printed output: %s", &out)
			}
		})
	}
}

func TestExecuteCompleteSendsTheVariables(t *testing.T) {
	var body struct {
		Variables map[string]any `json:"variables"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	file := processTestFile(t, "vars.json", `{"answer":"yes"}`)
	var out bytes.Buffer
	if err := executeComplete([]string{"7", "-engine", srv.URL, "-variables", file}, &out); err != nil {
		t.Fatal(err)
	}
	if body.Variables["answer"] != "yes" {
		t.Fatalf("variables: %v", body)
	}
}
