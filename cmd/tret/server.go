package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/triskill/tret/internal/config"
	"github.com/triskill/tret/internal/zen"
	"github.com/triskill/tret/pkg/journal"
	"github.com/triskill/tret/pkg/llm"
	"github.com/triskill/tret/pkg/shellexec"
	"github.com/triskill/tret/pkg/worker"
)

// drainTimeout bounds how long the server waits for running jobs after it
// stopped taking new ones.
const drainTimeout = 2 * time.Minute

// settleTime is how long the server waits for the jobs the engine handed out
// just before the shutdown to reach their handler.
const settleTime = 200 * time.Millisecond

// runServer hosts the selected generic workers and streams jobs from the
// engine. It deploys nothing; processes are deployed with 'tret deploy'.
func runServer(args []string) {
	cfg, err := config.FromFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "log level %q is not a level\n", cfg.LogLevel)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	// One OpenAI-compatible endpoint set serves the default profile: api_key
	// set -> api_base (OpenAI/OpenRouter/...); empty -> local Ollama /v1.
	base := cfg.APIBase
	if cfg.APIKey == "" {
		base = cfg.OllamaBase
	}
	profiles := make(map[string]llm.Profile, len(cfg.ModelProfiles)+1)
	for name, profile := range cfg.ModelProfiles {
		profiles[name] = profile
	}
	if _, ok := profiles["default"]; !ok {
		profiles["default"] = llm.Profile{BaseURL: base, Model: cfg.Model, APIKey: cfg.APIKey}
	}
	for name, profile := range profiles {
		if err := profile.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "model profile %q is unusable: %v\n", name, err)
			os.Exit(1)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The journal remembers the outcome of a step, so a job the engine hands
	// out again repeats its result instead of its work. It is keyed on the
	// engine job key.
	id := clientID(cfg)
	runtimes := map[string]time.Duration{llm.JobType: llm.MaxCallDuration}
	if containsType(cfg.Workers, shellexec.JobType) {
		runtimes[shellexec.JobType] = shellexec.MaxTimeout
	}
	var guard *journal.Guard
	if cfg.JournalDir != "" {
		// The claim on a step must outlast the work it protects, or another
		// worker may take the step over while the first is still working on
		// it, and the work runs twice.
		longest := longestRuntime(runtimes)
		if err := checkLease(cfg.JournalLease.Duration(), runtimes); err != nil {
			slog.Error("the journal lease is unusable", "err", err, "lease", cfg.JournalLease.String(), "longest_job", longest.String())
			os.Exit(1)
		}
		store, err := journal.NewFile(cfg.JournalDir)
		if err != nil {
			slog.Error("the journal is unusable", "err", err)
			os.Exit(1)
		}
		guard = journal.NewGuard(store, id, cfg.JournalLease.Duration())
		slog.Info("journal enabled", "dir", store.Dir(), "lease", cfg.JournalLease.String())
	}

	llmWorker := llm.NewWorker(profiles, guard)
	builtins := map[string]zen.WorkerFunc{llm.JobType: llmWorker.Handle}
	if containsType(cfg.Workers, shellexec.JobType) {
		shellWorker, err := shellexec.NewWorker(cfg.ExecRoot, guard)
		if err != nil {
			slog.Error("the exec worker is unusable", "err", err)
			os.Exit(1)
		}
		switch cfg.ExecWorkspace {
		case "shared":
		case "instance":
			shellWorker.Isolated = true
		default:
			slog.Error("exec_workspace must be shared or instance", "value", cfg.ExecWorkspace)
			os.Exit(1)
		}
		builtins[shellexec.JobType] = shellWorker.Handle
	}
	registry := worker.NewRegistry()
	if err := registerSelected(registry, builtins, runtimes, cfg); err != nil {
		slog.Error("the workers are unusable", "err", err)
		os.Exit(1)
	}
	slog.Info("workers registered", "types", registry.Types(), "lock_duration", cfg.LockDuration.String(), "max_active_jobs", cfg.MaxActiveJobs)

	options := registry.Options()
	deadline := time.Now().Add(60 * time.Second)
	for {
		conn, err := zen.DialGRPC(cfg.EngineGRPC)
		if err == nil {
			g := zen.NewGrpcClient(conn)
			_, err = g.RegisterWorkerWithOptions(ctx, id, registry.Handle, options...)
			if err == nil {
				defer conn.Close()
			} else {
				conn.Close()
			}
		}
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			slog.Error("the engine is not reachable", "addr", cfg.EngineGRPC, "err", err)
			os.Exit(1)
		}
		slog.Warn("waiting for the engine", "addr", cfg.EngineGRPC, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	slog.Info("server running", "client_id", id, "engine", cfg.EngineGRPC)
	<-ctx.Done()
	slog.Info("server stopping")
	// The workers keep their jobs when the stream breaks, so the shutdown
	// waits for the running ones instead of cutting a side effect short.
	// The settle time lets the jobs the engine handed out just before the
	// shutdown reach their handler; a job which never got that far is the
	// engine's to hand out again.
	time.Sleep(settleTime)
	drainCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	if err := registry.Drain(drainCtx); err != nil {
		slog.Warn("jobs are still running at the shutdown", "err", err)
	}
}

func containsType(selected, typ string) bool {
	for _, t := range strings.Split(selected, ",") {
		if strings.TrimSpace(t) == typ {
			return true
		}
	}
	return false
}

// longestRuntime returns the longest a served worker may run.
func longestRuntime(runtimes map[string]time.Duration) time.Duration {
	longest := time.Duration(0)
	for _, runtime := range runtimes {
		if runtime > longest {
			longest = runtime
		}
	}
	return longest
}

// checkLease reports whether the claim on a step may expire while the work
// it protects is still running. The lease must outlast the longest job of
// the workers, or another worker takes the step over and the work runs
// twice.
func checkLease(lease time.Duration, runtimes map[string]time.Duration) error {
	longest := longestRuntime(runtimes)
	if lease <= longest {
		return fmt.Errorf("the lease %s must outlast the longest job %s", lease, longest)
	}
	return nil
}

// clientID names this worker at the engine. It must be unique per process:
// the engine keeps one entry per client id in the round robin over the
// workers of a job type, so two processes with the same id share one slot
// and one of them silently receives no jobs at all.
var processClientID = sync.OnceValue(func() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "tret"
	}
	return fmt.Sprintf("tret-%s-%d-%d", host, os.Getpid(), time.Now().UnixNano()%1000000)
})

func clientID(cfg *config.Config) string {
	if cfg.ClientID != "" {
		return cfg.ClientID
	}
	return processClientID()
}

func registerSelected(registry *worker.Registry, builtins map[string]zen.WorkerFunc, runtimes map[string]time.Duration, cfg *config.Config) error {
	for _, typ := range strings.Split(cfg.Workers, ",") {
		typ = strings.TrimSpace(typ)
		handler, ok := builtins[typ]
		if !ok {
			return fmt.Errorf("unknown built-in worker %q", typ)
		}
		spec := worker.Spec{
			JobType:       typ,
			LockDuration:  cfg.LockDuration.Duration(),
			MaxActiveJobs: cfg.MaxActiveJobs,
			MaxRuntime:    runtimes[typ],
		}
		if err := registry.Register(spec, handler); err != nil {
			return err
		}
	}
	return nil
}
