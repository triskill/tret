package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	tret "github.com/triskill/tret"
	"github.com/triskill/tret/internal/brain"
	"github.com/triskill/tret/internal/config"
	"github.com/triskill/tret/internal/contextstore"
	"github.com/triskill/tret/internal/executor"
	"github.com/triskill/tret/internal/server"
	"github.com/triskill/tret/internal/zen"
	"github.com/triskill/tret/pkg/llm"
	"github.com/triskill/tret/pkg/worker"
)

// runServer hosts selected built-in workers. Bundled agent deployment is optional;
// arbitrary BPMN and external workers have independent lifecycles.
func runServer(args []string) {
	cfg := config.FromFlags(args)
	var runner *executor.Runner
	for _, typ := range strings.Split(cfg.Workers, ",") {
		if strings.TrimSpace(typ) == "agent-execute" {
			var err error
			runner, err = executor.NewRunner(cfg.Workspace)
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
			break
		}
	}
	// One OpenAI-compatible client serves both provider tiers: api_key
	// set → api_base (OpenAI/OpenRouter/...); empty → local Ollama /v1.
	base := cfg.APIBase
	if cfg.APIKey == "" {
		base = cfg.OllamaBase
	}
	var agentLLM brain.LLMProvider = brain.NewOpenAICompat(base, cfg.Model, cfg.APIKey)
	if os.Getenv("MOCK_LLM") == "1" {
		agentLLM = &brain.FinishLLM{}
	}
	ctxStore := contextstore.New(cfg.StateDir)
	srv := server.New(agentLLM, runner, ctxStore)
	srv.Limits = server.Limits{MaxToolCalls: cfg.MaxToolCalls, MaxModelTurns: cfg.MaxModelTurns, WallClockSec: cfg.WallClockSec}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	profiles := make(map[string]llm.Profile, len(cfg.ModelProfiles)+1)
	for name, profile := range cfg.ModelProfiles {
		profiles[name] = profile
	}
	if _, ok := profiles["default"]; !ok {
		profiles["default"] = llm.Profile{BaseURL: base, Model: cfg.Model, APIKey: cfg.APIKey}
	}
	llmWorker := llm.NewWorker(profiles)
	registry := worker.NewRegistry()
	builtins := map[string]zen.WorkerFunc{
		"agent-budget":      srv.CheckBudget,
		"agent-decide":      srv.Decide,
		"agent-execute":     srv.Execute,
		"agent-fail-report": srv.FailReport,
		"llm-call-v1":       llmWorker.Handle,
	}
	if err := registerSelected(registry, builtins, cfg.Workers); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	options := make([]zenclient.WorkerOption, 0, len(registry.Types()))
	for _, typ := range registry.Types() {
		options = append(options, zenclient.WithJobType(typ, zenclient.WithLockDuration(5*time.Minute)))
	}
	zc := zen.NewClient(cfg.EngineREST)
	deadline := time.Now().Add(60 * time.Second)
	for cfg.DeployAgent {
		key, created, err := zc.Deploy(tret.AgentLoopBPMN)
		if err == nil {
			fmt.Printf("deployed agent_loop: key=%d newVersion=%v\n", key, created)
			break
		}
		if time.Now().After(deadline) {
			fmt.Printf("deploy failed: %v\n", err)
			os.Exit(1)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}

	for {
		conn, err := zen.DialGRPC(cfg.EngineGRPC)
		if err == nil {
			g := zen.NewGrpcClient(conn)
			_, err = g.RegisterWorkerWithOptions(ctx, fmt.Sprintf("tret-server-%d", os.Getpid()), registry.Handle, options...)
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
}

func registerSelected(registry *worker.Registry, builtins map[string]zen.WorkerFunc, selected string) error {
	for _, typ := range strings.Split(selected, ",") {
		typ = strings.TrimSpace(typ)
		handler, ok := builtins[typ]
		if !ok {
			return fmt.Errorf("unknown built-in worker %q", typ)
		}
		if err := registry.Register(typ, handler); err != nil {
			return err
		}
	}
	return nil
}
