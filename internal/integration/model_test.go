package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tret "github.com/triskill/tret"
	"github.com/triskill/tret/internal/brain"
	"github.com/triskill/tret/internal/contextstore"
	"github.com/triskill/tret/internal/executor"
	"github.com/triskill/tret/internal/server"
	"github.com/triskill/tret/internal/zen"
)

// TestAgentLoopRealModel needs a dedicated engine with no other agent-* workers and
// a tool-capable OpenAI-compatible model. It executes model-generated bash in a
// temporary workspace; use only a trusted model endpoint.
//
// TRET_MODEL_TEST=1 TRET_MODEL=<model> go test ./internal/integration -run '^TestAgentLoopRealModel$' -v -count=1
//
// TRET_MODEL_BASE defaults to Ollama's http://localhost:11434/v1; optionally set
// TRET_MODEL_API_KEY. Engine addresses use TRET_INTEGRATION_REST/GRPC as above.
func TestAgentLoopRealModel(t *testing.T) {
	if os.Getenv("TRET_MODEL_TEST") != "1" {
		t.Skip("set TRET_MODEL_TEST=1 to run the real-model acceptance test")
	}
	model := os.Getenv("TRET_MODEL")
	if model == "" {
		t.Fatal("TRET_MODEL is required when TRET_MODEL_TEST=1")
	}
	base := os.Getenv("TRET_MODEL_BASE")
	if base == "" {
		base = "http://localhost:11434/v1"
	}
	rest, addr := os.Getenv("TRET_INTEGRATION_REST"), os.Getenv("TRET_INTEGRATION_GRPC")
	if rest == "" {
		rest = "http://localhost:8080"
	}
	if addr == "" {
		addr = "localhost:9090"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	client := zen.NewClient(strings.TrimRight(rest, "/"))
	client.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	definition, _, err := client.Deploy(tret.AgentLoopBPMN)
	if err != nil {
		t.Fatalf("deploy embedded BPMN (start dedicated engine first): %v", err)
	}
	conn, err := zen.DialGRPC(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	workspace := t.TempDir()
	input := "Hello, tret!\nMixed Case 123.\n"
	if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	runner, err := executor.NewRunner(workspace)
	if err != nil {
		t.Fatal(err)
	}
	store := contextstore.New(t.TempDir())
	llm := brain.NewOpenAICompat(strings.TrimRight(base, "/"), model, os.Getenv("TRET_MODEL_API_KEY"))
	srv := server.New(llm, runner, store)
	srv.Limits = server.Limits{MaxModelTurns: 8, MaxToolCalls: 6, WallClockSec: 180}
	bk := fmt.Sprintf("model-acceptance-%d", time.Now().UnixNano())
	worker := func(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
		var vars map[string]any
		if err := json.Unmarshal(job.InputVariables, &vars); err != nil {
			return nil, &zen.WorkerError{Err: err}
		}
		if vars["business_key"] != bk {
			return nil, &zen.WorkerError{Err: fmt.Errorf("unexpected run %v; use a dedicated engine", vars["business_key"])}
		}
		switch job.GetType() {
		case "agent-budget":
			return srv.CheckBudget(ctx, job)
		case "agent-decide":
			return srv.Decide(ctx, job)
		case "agent-execute":
			return srv.Execute(ctx, job)
		case "agent-fail-report":
			return srv.FailReport(ctx, job)
		default:
			return nil, &zen.WorkerError{Err: fmt.Errorf("unexpected job type %s", job.GetType())}
		}
	}
	if err := zen.Register(ctx, zen.NewGrpcClient(conn), bk, worker,
		"agent-budget", "agent-decide", "agent-execute", "agent-fail-report"); err != nil {
		t.Fatal(err)
	}
	key, err := client.StartInstance(definition, bk, map[string]any{"business_key": bk})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.CancelInstance(key) })
	job := awaitReply(t, client, key, 0)
	prompt := `Use bash to read input.txt in the workspace. Then create output.txt containing exactly the input converted to uppercase, preserving punctuation and newlines. In a separate bash call, verify output.txt against the uppercase input and show its contents. Use at least two bash calls. Do not ask questions. Only after successful verification, finish with the required finish route JSON.`
	if err := client.CompleteJob(job.Key, map[string]any{"user_message": prompt}); err != nil {
		t.Fatal(err)
	}

	// The scripted-test completion helper has a 20s deadline; real models get
	// the full acceptance-test deadline instead.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		inst, err := client.GetInstance(key)
		if err != nil {
			t.Fatal(err)
		}
		if inst.State == "completed" {
			transcript, err := store.Load(bk)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(transcript, "stdout:\n"+strings.ToUpper(input)) {
				t.Errorf("transcript missing observed uppercase stdout:\n%s", transcript)
			}
			if inst.Variables["run_outcome"] != "success" {
				t.Fatalf("expected success: vars=%#v\ntranscript:\n%s", inst.Variables, transcript)
			}
			tools, ok := inst.Variables["tool_calls"].(float64)
			if !ok || tools < 2 {
				t.Errorf("tool_calls=%v, want at least 2\ntranscript:\n%s", inst.Variables["tool_calls"], transcript)
			}
			output, err := os.ReadFile(filepath.Join(workspace, "output.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if string(output) != strings.ToUpper(input) {
				t.Errorf("output.txt=%q, want %q", output, strings.ToUpper(input))
			}
			return
		}
		if inst.State == "failed" || inst.State == "terminated" {
			t.Fatalf("unexpected engine state: %#v", inst)
		}
		jobs, err := client.ListJobs(key)
		if err != nil {
			t.Fatal(err)
		}
		for _, pending := range jobs {
			if pending.Type == "agent-reply" && pending.State == "active" && pending.Key != job.Key {
				transcript, _ := store.Load(bk)
				t.Fatalf("model requested human input instead of finishing: job=%#v\ntranscript:\n%s", pending, transcript)
			}
		}
		incidents, err := client.ListIncidents(key)
		if err != nil {
			t.Fatal(err)
		}
		if len(incidents) > 0 {
			t.Fatalf("engine incidents: %#v", incidents)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			transcript, _ := store.Load(bk)
			jobs, _ := client.ListJobs(key)
			t.Fatalf("180s deadline: instance=%#v jobs=%#v\ntranscript:\n%s", inst, jobs, transcript)
		}
	}
}
