package shellexec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
)

var _ zenclient.WorkerFunc = (*Worker)(nil).Handle

func handle(t *testing.T, root string, input map[string]any) (map[string]any, *zenclient.WorkerError) {
	t.Helper()
	w, err := NewWorker(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return w.Handle(context.Background(), &proto.WaitingJob{InputVariables: raw})
}

func TestWorkerRunsCommand(t *testing.T) {
	out, workerErr := handle(t, t.TempDir(), map[string]any{
		"exec_request": map[string]any{"command": "echo hi", "timeout_sec": 5},
	})
	if workerErr != nil {
		t.Fatalf("worker error: %v", workerErr)
	}
	result, _ := out["exec_result"].(map[string]any)
	if result == nil || result["exit_code"] != 0 || result["stdout"] != "hi\n" || result["timed_out"] != false {
		t.Fatalf("unexpected result: %v", out)
	}
}

func TestWorkerReportsFailureAsNormalResult(t *testing.T) {
	out, workerErr := handle(t, t.TempDir(), map[string]any{
		"exec_request": map[string]any{"command": "exit 3", "timeout_sec": 5},
	})
	if workerErr != nil {
		t.Fatalf("non-zero exit must be a normal result, got %v", workerErr)
	}
	result := out["exec_result"].(map[string]any)
	if result["exit_code"] != 3 {
		t.Fatalf("unexpected result: %v", out)
	}
}

func TestWorkerRejectsBadRequests(t *testing.T) {
	w, err := NewWorker(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bad := []map[string]any{
		{},                                 // missing exec_request
		{"exec_request": map[string]any{}}, // missing command
		{"exec_request": map[string]any{"command": ""}}, // empty command
		{"exec_request": map[string]any{"command": "ls", "timeout_sec": 0}},
		{"exec_request": map[string]any{"command": "ls", "timeout_sec": 121}},
		{"exec_request": map[string]any{"command": "ls", "nope": 1}}, // unknown field
		{"exec_request": "not an object"},
	}
	for i, input := range bad {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		_, workerErr := w.Handle(context.Background(), &proto.WaitingJob{InputVariables: raw})
		if workerErr == nil || workerErr.ErrorCode != "invalid_input" || workerErr.Retries == nil || *workerErr.Retries != 0 {
			t.Fatalf("case %d: expected invalid_input with zero retries, got %v", i, workerErr)
		}
	}
}

func TestWorkerTimeout(t *testing.T) {
	out, workerErr := handle(t, t.TempDir(), map[string]any{
		"exec_request": map[string]any{"command": "sleep 5", "timeout_sec": 0.2},
	})
	if workerErr != nil {
		t.Fatalf("timeout is an ordinary result, got %v", workerErr)
	}
	result := out["exec_result"].(map[string]any)
	if result["timed_out"] != true {
		t.Fatalf("expected timed_out, got %v", out)
	}
}

// The command must survive a broken job stream. The engine cancels the job
// context when the stream to the worker breaks and hands the job out again;
// a command killed halfway is a partially applied side effect which then runs
// a second time.
func TestCommandSurvivesCancelledJobContext(t *testing.T) {
	w, err := NewWorker(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"exec_request": map[string]any{"command": "echo hi", "timeout_sec": 5}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, workerErr := w.Handle(ctx, &proto.WaitingJob{InputVariables: raw})
	if workerErr != nil {
		t.Fatalf("cancelled job context aborted the command: %v", workerErr)
	}
	result := out["exec_result"].(map[string]any)
	if result["stdout"] != "hi\n" || result["cancelled"] != false {
		t.Fatalf("unexpected result: %v", result)
	}
}
