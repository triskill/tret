package zen

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
