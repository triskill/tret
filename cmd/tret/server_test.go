package main

import (
	"context"
	"testing"

	"github.com/triskill/tret/internal/zen"
	"github.com/triskill/tret/pkg/worker"
)

func TestRegisterSelectedWorkers(t *testing.T) {
	builtin := map[string]zen.WorkerFunc{"llm-call-v1": func(context.Context, *zen.WaitingJob) (map[string]any, *zen.WorkerError) { return nil, nil }, "agent-execute": func(context.Context, *zen.WaitingJob) (map[string]any, *zen.WorkerError) { return nil, nil }}
	r := worker.NewRegistry()
	if err := registerSelected(r, builtin, " llm-call-v1 "); err != nil {
		t.Fatal(err)
	}
	if types := r.Types(); len(types) != 1 || types[0] != "llm-call-v1" {
		t.Fatalf("unexpected subscriptions: %v", types)
	}
	for _, selected := range []string{"", "unknown", "llm-call-v1,llm-call-v1"} {
		if err := registerSelected(worker.NewRegistry(), builtin, selected); err == nil {
			t.Fatalf("accepted %q", selected)
		}
	}
}
