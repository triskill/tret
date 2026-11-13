package zen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	if err := c.CompleteJob(789, map[string]any{"route": "finish"}); err != nil {
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
			job := Job{Key: int64(i + 1), Type: "agent-decide", State: "completed"}
			if i == total-1 {
				job.Type, job.State = "agent-reply", "active"
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
	if last.Type != "agent-reply" || last.State != "active" || last.InputVariables["assistant_text"] != "Please reply" {
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
