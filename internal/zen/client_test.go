package zen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestDeploy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"processDefinitionKey": 123})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	key, created, err := c.Deploy([]byte("bpmn"))
	if err != nil || key != 123 || !created {
		t.Fatalf("key=%d created=%v err=%v", key, created, err)
	}
}

func TestStartInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"key": 456})
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	key, err := c.StartInstance(123, "bk", map[string]any{"goal": "g"})
	if err != nil || key != 456 {
		t.Fatalf("key=%d err=%v", key, err)
	}
}

func TestCompleteJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	if err := c.CompleteJob(789, map[string]any{"answer": "ok"}); err != nil {
		t.Fatal(err)
	}
}

func TestListJobsPagination(t *testing.T) {
	const total = 103
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/process-instances/456/jobs" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || page != requests+1 {
			t.Errorf("page=%d err=%v, want %d", page, err, requests+1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests++
		const size = 10
		var items []Job
		for i := (page - 1) * size; i < page*size && i < total; i++ {
			job := Job{Key: int64(i + 1), Type: "llm-call-v1", State: "completed"}
			if i == total-1 {
				job.Type, job.State = "user-reply", "active"
				job.InputVariables = map[string]any{"assistant_text": "Please reply"}
			}
			items = append(items, job)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"items": items, "page": page, "size": size, "totalCount": total,
		})
	}))
	defer srv.Close()

	jobs, err := NewClient(srv.URL).ListJobs(456)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != total || requests != 11 {
		t.Fatalf("jobs=%d requests=%d, want %d and 11", len(jobs), requests, total)
	}
	for i, job := range jobs {
		if job.Key != int64(i+1) {
			t.Fatalf("job %d key=%d", i, job.Key)
		}
	}
	last := jobs[len(jobs)-1]
	if last.Type != "user-reply" || last.State != "active" || last.InputVariables["assistant_text"] != "Please reply" {
		t.Fatalf("missing active reply: %#v", last)
	}
}

func TestListJobsLaterPageError(t *testing.T) {
	for _, body := range []string{"upstream unavailable", "invalid JSON"} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page") == "1" {
					fmt.Fprint(w, `{"items":[{"key":1}],"page":1,"size":1,"totalCount":2}`)
					return
				}
				if body == "upstream unavailable" {
					w.WriteHeader(http.StatusBadGateway)
				}
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			jobs, err := NewClient(srv.URL).ListJobs(456)
			if err == nil || jobs != nil {
				t.Fatalf("jobs=%v err=%v, want error without partial results", jobs, err)
			}
		})
	}
}

func TestMalformedEngineURL(t *testing.T) {
	c := NewClient("://bad")
	if _, err := c.StartInstance(1, "", nil); err == nil {
		t.Fatal("malformed start URL accepted")
	}
	if err := c.CompleteJob(1, nil); err == nil {
		t.Fatal("malformed completion URL accepted")
	}
}

// The engine answers the list of instances with a page per partition.
func TestListInstancesFollowsThePages(t *testing.T) {
	const total = 12
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/process-instances" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if state := r.URL.Query().Get("state"); state != "" && state != "active" {
			t.Errorf("unexpected state filter: %q", state)
		}
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || page != requests+1 {
			t.Errorf("page=%d err=%v, want %d", page, err, requests+1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests++
		const size = 5
		var items []Instance
		for i := (page - 1) * size; i < page*size && i < total; i++ {
			items = append(items, Instance{
				Key: int64(i + 1), State: "active", BusinessKey: fmt.Sprintf("bk-%d", i+1),
				ProcessDefinitionKey: 7, BPMNProcessID: "invoice", IncidentCount: i,
			})
		}
		json.NewEncoder(w).Encode(map[string]any{
			"partitions": []any{map[string]any{"items": items, "partition": 1, "totalCount": total}},
			"count":      len(items), "page": page, "size": size, "totalCount": total,
		})
	}))
	defer srv.Close()

	instances, err := NewClient(srv.URL).ListInstances("active")
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != total || requests != 3 {
		t.Fatalf("instances=%d requests=%d, want %d and 3", len(instances), requests, total)
	}
	for i, instance := range instances {
		if instance.Key != int64(i+1) || instance.BusinessKey != fmt.Sprintf("bk-%d", i+1) {
			t.Fatalf("instance %d: %#v", i, instance)
		}
		if instance.ProcessDefinitionKey != 7 || instance.BPMNProcessID != "invoice" || instance.IncidentCount != i {
			t.Fatalf("instance %d: %#v", i, instance)
		}
	}
}

func TestListInstancesRejectsABadAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not JSON")
	}))
	defer srv.Close()
	instances, err := NewClient(srv.URL).ListInstances("")
	if err == nil || instances != nil {
		t.Fatalf("instances=%v err=%v, want error without partial results", instances, err)
	}
}

func TestResolveIncident(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/incidents/42/resolve" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	if err := NewClient(srv.URL).ResolveIncident(42, 2); err != nil {
		t.Fatal(err)
	}
	if body["retries"] != float64(2) {
		t.Fatalf("body: %v", body)
	}
}

func TestResolveIncidentRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, "the incident is already resolved")
	}))
	defer srv.Close()
	err := NewClient(srv.URL).ResolveIncident(42, 1)
	if err == nil || !strings.Contains(err.Error(), "already resolved") {
		t.Fatalf("err=%v", err)
	}
}

// The engine does not send a state for an incident: an incident which was
// resolved has a resolvedAt.
func TestListIncidentsReadsTheResolvedAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"key":1,"message":"failed","elementId":"Probe","jobKey":2,"processInstanceKey":3},{"key":4,"message":"resolved","resolvedAt":"2026-11-24T19:39:38Z"}],"count":2,"page":1,"size":10,"totalCount":2}`)
	}))
	defer srv.Close()
	incidents, err := NewClient(srv.URL).ListIncidents(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents) != 2 {
		t.Fatalf("incidents=%d", len(incidents))
	}
	if incidents[0].ResolvedAt != nil {
		t.Errorf("the open incident is resolved: %v", incidents[0].ResolvedAt)
	}
	if incidents[1].ResolvedAt == nil {
		t.Error("the resolved incident has no resolvedAt")
	}
	if incidents[0].ElementID != "Probe" || incidents[0].JobKey != 2 {
		t.Errorf("incident: %#v", incidents[0])
	}
}
