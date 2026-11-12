package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	tret "github.com/triskill/tret"
	"github.com/triskill/tret/internal/brain"
	"github.com/triskill/tret/internal/config"
	"github.com/triskill/tret/internal/contextstore"
	"github.com/triskill/tret/internal/executor"
	"github.com/triskill/tret/internal/server"
	"github.com/triskill/tret/internal/zen"
)

// runServer is the worker daemon: it deploys the embedded agent_loop process
// (idempotent), registers gRPC job workers for decide/fail-report, and owns
// durable run state.
func runServer(args []string) {
	cfg := config.FromFlags(args)
	runner, err := executor.NewRunner(cfg.Workspace)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	var llm brain.LLMProvider
	if cfg.APIKey != "" {
		llm = brain.NewOpenAICompat(cfg.APIBase, cfg.Model, cfg.APIKey)
	} else {
		llm = brain.NewOllamaLLM(cfg.OllamaBase, cfg.Model)
	}
	if os.Getenv("MOCK_LLM") == "1" {
		llm = &brain.FinishLLM{}
	}
	ctxStore := contextstore.New(cfg.StateDir)
	srv := server.New(llm, runner, ctxStore)

	zc := zen.NewClient(cfg.EngineREST)
	// A fresh compose stack takes a while to boot: retry the idempotent
	// deploy and worker registration until the engine answers (60s cap).
	deadline := time.Now().Add(60 * time.Second)
	for {
		key, created, err := zc.Deploy(tret.AgentLoopBPMN)
		if err == nil {
			fmt.Printf("deployed agent_loop: key=%d newVersion=%v\n", key, created)
			break
		}
		if time.Now().After(deadline) {
			fmt.Printf("deploy failed: %v\n", err)
			os.Exit(1)
		}
		time.Sleep(time.Second)
	}

	ctx := context.Background()
	worker := func(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
		if job != nil && job.Type != nil {
			switch *job.Type {
			case "agent-fail-report":
				return srv.FailReport(ctx, job)
			}
		}
		return srv.Decide(ctx, job)
	}
	for {
		conn, err := zen.DialGRPC(cfg.EngineGRPC)
		if err == nil {
			g := zen.NewGrpcClient(conn)
			_, err = g.RegisterWorkerWithOptions(ctx, fmt.Sprintf("tret-server-%d", os.Getpid()), worker,
				zenclient.WithJobType("agent-decide", zenclient.WithLockDuration(5*time.Minute)),
				zenclient.WithJobType("agent-fail-report", zenclient.WithLockDuration(5*time.Minute)),
			)
		}
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Printf("waiting for engine gRPC (%v)\n", err)
		time.Sleep(time.Second)
	}
	fmt.Println("server running")
	for {
		time.Sleep(time.Hour)
	}
}
