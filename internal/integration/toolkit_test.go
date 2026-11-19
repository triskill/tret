package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/triskill/tret/examples/classifier"
	"github.com/triskill/tret/examples/classifier/report"
	"github.com/triskill/tret/internal/zen"
	"github.com/triskill/tret/pkg/llm"
)

// TestToolkitClassifierLive proves task-local mappings, schema validation,
// engine-owned routing, and an independently registered application worker.
func TestToolkitClassifierLive(t *testing.T) {
	if os.Getenv("TRET_INTEGRATION") != "1" {
		t.Skip("set TRET_INTEGRATION=1 to use a dedicated live ZenBPM engine")
	}
	rest, addr := os.Getenv("TRET_INTEGRATION_REST"), os.Getenv("TRET_INTEGRATION_GRPC")
	if rest == "" {
		rest = "http://localhost:8080"
	}
	if addr == "" {
		addr = "localhost:9090"
	}
	client := zen.NewClient(strings.TrimRight(rest, "/"))
	client.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	definition, _, err := client.Deploy(classifier.BPMN)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := zen.DialGRPC(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var modelCalls, reportCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			ResponseFormat map[string]any `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("provider path = %s", r.URL.Path)
		}
		if body.ResponseFormat["type"] != "json_schema" {
			t.Errorf("schema not sent: %#v", body.ResponseFormat)
		}
		content := `{"approved":false}`
		if len(body.Messages) != 1 {
			t.Errorf("messages = %#v", body.Messages)
		} else {
			switch body.Messages[0].Content {
			case "accept":
				content = `{"approved":true}`
			case "invalid":
				content = `{"approved":"yes"}`
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "mock-classifier", "model": "mock", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
	defer provider.Close()
	worker := llm.NewWorker(map[string]llm.Profile{"default": {BaseURL: provider.URL, Model: "mock"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	grpcClient := zen.NewGrpcClient(conn)
	if err := zen.Register(ctx, grpcClient, fmt.Sprintf("toolkit-llm-%d", time.Now().UnixNano()), worker.Handle, llm.JobType); err != nil {
		t.Fatal(err)
	}
	if err := zen.Register(ctx, grpcClient, fmt.Sprintf("toolkit-report-%d", time.Now().UnixNano()), func(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
		reportCalls.Add(1)
		return report.Handle(ctx, job)
	}, "example-report-v1"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"accept", "deny", "invalid"} {
		t.Run(name, func(t *testing.T) {
			beforeModel, beforeReport := modelCalls.Load(), reportCalls.Load()
			request := map[string]any{"model_profile": "default", "messages": []any{map[string]any{"role": "user", "content": name}}, "output_schema": map[string]any{"type": "object", "properties": map[string]any{"approved": map[string]any{"type": "boolean"}}, "required": []string{"approved"}, "additionalProperties": false}}
			key, err := client.StartInstance(definition, fmt.Sprintf("toolkit-%s-%d", name, time.Now().UnixNano()), map[string]any{"request": request})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.CancelInstance(key) })
			if name == "invalid" {
				awaitToolkitIncident(t, client, key)
			} else {
				instance := awaitCompleted(t, client, key)
				classification, ok := instance.Variables["classification"].(map[string]any)
				if !ok {
					t.Fatalf("classification: %#v", instance.Variables)
				}
				structured, ok := classification["structured_output"].(map[string]any)
				if !ok || structured["approved"] != (name == "accept") {
					t.Fatalf("structured output: %#v", classification)
				}
				if name == "accept" && instance.Variables["report"] != "Queue: accepted\nApproved: true" {
					t.Fatalf("custom report missing: %#v", instance.Variables)
				}
				if name == "deny" && instance.Variables["report"] != nil {
					t.Fatalf("denied request ran custom worker: %#v", instance.Variables)
				}
			}
			if got := modelCalls.Load() - beforeModel; got != 1 {
				t.Errorf("model calls=%d, want 1", got)
			}
			wantReports := int32(0)
			if name == "accept" {
				wantReports = 1
			}
			if got := reportCalls.Load() - beforeReport; got != wantReports {
				t.Errorf("report calls=%d, want %d", got, wantReports)
			}
		})
	}
}

func awaitToolkitIncident(t *testing.T, client *zen.Client, key int64) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		incidents, err := client.ListIncidents(key)
		if err != nil {
			t.Fatal(err)
		}
		if len(incidents) > 0 {
			return
		}
		instance, err := client.GetInstance(key)
		if err != nil {
			t.Fatal(err)
		}
		if instance.State == "completed" || instance.State == "terminated" || instance.State == "failed" {
			t.Fatalf("invalid output did not create an incident: %#v", instance)
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("timed out waiting for invalid output incident")
		}
	}
}

// awaitCompleted polls until the instance reaches the completed state.
func awaitCompleted(t *testing.T, client *zen.Client, key int64) *zen.Instance {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		instance, err := client.GetInstance(key)
		if err != nil {
			t.Fatal(err)
		}
		if instance.State == "failed" || instance.State == "terminated" {
			incidents, _ := client.ListIncidents(key)
			t.Fatalf("unexpected engine state %q: incidents=%#v", instance.State, incidents)
		}
		if instance.State == "completed" {
			return instance
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			jobs, _ := client.ListJobs(key)
			t.Fatalf("timed out waiting for instance %d: instance=%#v jobs=%#v", key, instance, jobs)
		}
	}
}
