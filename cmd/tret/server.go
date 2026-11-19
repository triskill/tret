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
	"github.com/triskill/tret/internal/config"
	"github.com/triskill/tret/internal/zen"
	"github.com/triskill/tret/pkg/llm"
	"github.com/triskill/tret/pkg/shellexec"
	"github.com/triskill/tret/pkg/worker"
)

// runServer hosts the selected generic workers and streams jobs from the
// engine. It deploys nothing; processes are deployed with 'tret deploy'.
func runServer(args []string) {
	cfg := config.FromFlags(args)

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	llmWorker := llm.NewWorker(profiles)
	builtins := map[string]zen.WorkerFunc{llm.JobType: llmWorker.Handle}
	if containsType(cfg.Workers, shellexec.JobType) {
		shellWorker, err := shellexec.NewWorker(cfg.ExecRoot)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		builtins[shellexec.JobType] = shellWorker.Handle
	}
	registry := worker.NewRegistry()
	if err := registerSelected(registry, builtins, cfg.Workers); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	options := make([]zenclient.WorkerOption, 0, len(registry.Types()))
	for _, typ := range registry.Types() {
		options = append(options, zenclient.WithJobType(typ, zenclient.WithLockDuration(5*time.Minute)))
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		conn, err := zen.DialGRPC(cfg.EngineGRPC)
		if err == nil {
			g := zen.NewGrpcClient(conn)
			_, err = g.RegisterWorkerWithOptions(ctx, fmt.Sprintf("tret-%d", os.Getpid()), registry.Handle, options...)
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

func containsType(selected, typ string) bool {
	for _, t := range strings.Split(selected, ",") {
		if strings.TrimSpace(t) == typ {
			return true
		}
	}
	return false
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
