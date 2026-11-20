package main

import (
	"context"
	"testing"
	"time"

	"github.com/triskill/tret/internal/config"
	"github.com/triskill/tret/internal/zen"
	"github.com/triskill/tret/pkg/llm"
	"github.com/triskill/tret/pkg/shellexec"
	"github.com/triskill/tret/pkg/worker"
)

func TestRegisterSelectedWorkers(t *testing.T) {
	builtin := map[string]zen.WorkerFunc{"llm-call-v1": func(context.Context, *zen.WaitingJob) (map[string]any, *zen.WorkerError) { return nil, nil }, "exec-command-v1": func(context.Context, *zen.WaitingJob) (map[string]any, *zen.WorkerError) { return nil, nil }}
	runtimes := map[string]time.Duration{llm.JobType: llm.MaxCallDuration, shellexec.JobType: shellexec.MaxTimeout}
	cfg := &config.Config{Workers: "llm-call-v1", LockDuration: config.Duration(worker.DefaultLockDuration)}
	r := worker.NewRegistry()
	if err := registerSelected(r, builtin, runtimes, cfg); err != nil {
		t.Fatal(err)
	}
	if types := r.Types(); len(types) != 1 || types[0] != "llm-call-v1" {
		t.Fatalf("unexpected subscriptions: %v", types)
	}
	for _, selected := range []string{"", "unknown", "llm-call-v1,llm-call-v1"} {
		bad := &config.Config{Workers: selected, LockDuration: config.Duration(worker.DefaultLockDuration)}
		if err := registerSelected(worker.NewRegistry(), builtin, runtimes, bad); err == nil {
			t.Fatalf("accepted %q", selected)
		}
	}
}

// The lock must outlast the longest job the worker runs, or the engine hands
// the job to a second worker while the first is still busy.
func TestRegisterSelectedRefusesShortLock(t *testing.T) {
	builtin := map[string]zen.WorkerFunc{llm.JobType: func(context.Context, *zen.WaitingJob) (map[string]any, *zen.WorkerError) { return nil, nil }}
	runtimes := map[string]time.Duration{llm.JobType: llm.MaxCallDuration}
	cfg := &config.Config{Workers: "llm-call-v1", LockDuration: config.Duration(time.Minute)}
	if err := registerSelected(worker.NewRegistry(), builtin, runtimes, cfg); err == nil {
		t.Fatal("lock shorter than the worker runtime accepted")
	}
}

func TestClientIDIsUniquePerProcess(t *testing.T) {
	cfg := &config.Config{}
	first := clientID(cfg)
	if first == "" {
		t.Fatal("empty client id")
	}
	if again := clientID(cfg); again != first {
		t.Fatalf("client id is not stable: %q vs %q", first, again)
	}
	cfg.ClientID = "named-worker"
	if got := clientID(cfg); got != "named-worker" {
		t.Fatalf("configured client id ignored: %q", got)
	}
}
