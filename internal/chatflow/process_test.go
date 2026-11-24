package chatflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/triskill/tret/internal/zen"
	"github.com/triskill/tret/pkg/shellexec"
)

// TestChatLive exercises engine-owned routing with actual shell execution.
func TestChatLive(t *testing.T) {
	if os.Getenv("TRET_INTEGRATION") != "1" {
		t.Skip("set TRET_INTEGRATION=1 for a live ZenBPM engine")
	}
	rest, addr := os.Getenv("TRET_INTEGRATION_REST"), os.Getenv("TRET_INTEGRATION_GRPC")
	if rest == "" {
		rest = "http://localhost:8080"
	}
	if addr == "" {
		addr = "localhost:9090"
	}
	client := zen.NewClient(rest)
	conn, err := zen.DialGRPC(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	grpc := zen.NewGrpcClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	suffix := fmt.Sprint(time.Now().UnixNano())
	modelType, execType := "chat-model-"+suffix, "chat-exec-"+suffix
	process := strings.ReplaceAll(string(BPMN), "llm-call-v1", modelType)
	process = strings.ReplaceAll(process, "exec-command-v1", execType)
	process = strings.ReplaceAll(process, "tret_chat", "tret_chat_"+suffix)
	definition, _, err := client.Deploy([]byte(process))
	if err != nil {
		t.Fatal(err)
	}
	var modelCalls, execCalls atomic.Int32
	if err := zen.Register(ctx, grpc, "model-"+suffix, func(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
		modelCalls.Add(1)
		var vars struct {
			Request struct {
				Action string `json:"action"`
			} `json:"llm_request"`
		}
		if err := json.Unmarshal(job.GetInputVariables(), &vars); err != nil {
			t.Error(err)
		}
		return map[string]any{"llm_result": map[string]any{"structured_output": map[string]any{"action": vars.Request.Action, "command": "printf chat-tested", "answer": "done"}}}, nil
	}, modelType); err != nil {
		t.Fatal(err)
	}
	shell, err := shellexec.NewWorker(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := zen.Register(ctx, grpc, "exec-"+suffix, func(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
		execCalls.Add(1)
		return shell.Handle(ctx, job)
	}, execType); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"answer", "approve", "deny", "budget"} {
		t.Run(name, func(t *testing.T) {
			beforeModel, beforeExec := modelCalls.Load(), execCalls.Load()
			budget := 8
			if name == "budget" {
				budget = 1
			}
			key, err := client.StartInstance(definition, "chat-"+name+suffix, map[string]any{"request": map[string]any{}, "response": nil, "execution": nil, "turn": 0, "max_turns": budget, "quit": false})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.CancelInstance(key) })
			complete := func(job zen.Job, vars map[string]any) {
				t.Helper()
				if err := client.CompleteJob(job.Key, vars); err != nil {
					t.Fatal(err)
				}
			}
			input := awaitChatJob(t, client, key, "tret-chat-input")
			action := "shell"
			if name == "answer" {
				action = "answer"
			}
			complete(input, map[string]any{"request": map[string]any{"action": action}, "quit": false, "turn": 0})
			if name == "approve" || name == "deny" {
				approval := awaitChatJob(t, client, key, "tret-chat-approve")
				response, ok := approval.InputVariables["response"].(map[string]any)
				if !ok || response["structured_output"] == nil {
					t.Fatalf("approval response missing: %#v", approval.InputVariables)
				}
				complete(approval, map[string]any{"approved": name == "approve", "request": map[string]any{"action": "answer"}})
				if name == "approve" {
					observation := awaitChatJob(t, client, key, "tret-chat-observe")
					execution, ok := observation.InputVariables["execution"].(map[string]any)
					if !ok || execution["stdout"] != "chat-tested" || execution["exit_code"] != float64(0) {
						t.Fatalf("execution: %#v", observation.InputVariables)
					}
					complete(observation, map[string]any{"request": map[string]any{"action": "answer"}})
				}
			}
			input = awaitChatJob(t, client, key, "tret-chat-input")
			wantModels := int32(1)
			if name == "approve" || name == "deny" {
				wantModels = 2
			}
			if got := modelCalls.Load() - beforeModel; got != wantModels {
				t.Errorf("model calls=%d want %d", got, wantModels)
			}
			wantExec := int32(0)
			if name == "approve" {
				wantExec = 1
			}
			if got := execCalls.Load() - beforeExec; got != wantExec {
				t.Errorf("exec calls=%d want %d", got, wantExec)
			}
			if input.InputVariables["turn"] != float64(wantModels) {
				t.Errorf("turn=%#v", input.InputVariables["turn"])
			}
			complete(input, map[string]any{"request": map[string]any{}, "quit": true, "turn": 0})
			awaitChatCompleted(t, client, key)
		})
	}
}

func awaitChatJob(t *testing.T, client *zen.Client, key int64, typ string) zen.Job {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		jobs, err := client.ListJobs(key)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if job.Type == typ && job.State == "active" {
				return job
			}
		}
		incidents, err := client.ListIncidents(key)
		if err != nil {
			t.Fatal(err)
		}
		if len(incidents) > 0 {
			t.Fatalf("incidents: %#v", incidents)
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("waiting for %s: jobs=%#v", typ, jobs)
		}
	}
}

func awaitChatCompleted(t *testing.T, client *zen.Client, key int64) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		instance, err := client.GetInstance(key)
		if err != nil {
			t.Fatal(err)
		}
		if instance.State == "completed" {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("instance not completed: %#v", instance)
		}
	}
}
