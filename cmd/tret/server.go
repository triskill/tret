package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/triskill/tret/internal/config"
	"github.com/triskill/tret/internal/zen"
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

	llmWorker := llm.NewWorker(profiles)
	builtins := map[string]zen.WorkerFunc{llm.JobType: llmWorker.Handle}
	runtimes := map[string]time.Duration{llm.JobType: llm.MaxCallDuration}
	if containsType(cfg.Workers, shellexec.JobType) {
		shellWorker, err := shellexec.NewWorker(cfg.ExecRoot)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		builtins[shellexec.JobType] = shellWorker.Handle
		runtimes[shellexec.JobType] = shellexec.MaxTimeout
	}
	registry := worker.NewRegistry()
	if err := registerSelected(registry, builtins, runtimes, cfg); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	options := registry.Options()
	deadline := time.Now().Add(60 * time.Second)
	for {
		conn, err := zen.DialGRPC(cfg.EngineGRPC)
		if err == nil {
			g := zen.NewGrpcClient(conn)
			_, err = g.RegisterWorkerWithOptions(ctx, clientID(cfg), registry.Handle, options...)
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
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Printf("waiting for engine gRPC (%v)\n", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	fmt.Println("server running")
	<-ctx.Done()
	fmt.Println("server stopping")
	// The workers keep their jobs when the stream breaks, so the shutdown
	// waits for the running ones instead of cutting a side effect short.
	// The settle time lets the jobs the engine handed out just before the
	// shutdown reach their handler; a job which never got that far is the
	// engine's to hand out again.
	time.Sleep(settleTime)
	drainCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	if err := registry.Drain(drainCtx); err != nil {
		fmt.Printf("jobs still running at shutdown: %v\n", err)
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
