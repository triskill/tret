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

func main() {
	cfg := config.FromFlags()
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

	// Deploy the embedded process definition (idempotent: identical
	// content returns the existing processDefinitionKey).
	zc := zen.NewClient(cfg.EngineREST)
	if _, _, err := zc.Deploy(tret.AgentLoopBPMN); err != nil {
		fmt.Printf("deploy warning: %v\n", err)
	}

	conn, err := zen.DialGRPC(cfg.EngineGRPC)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	g := zen.NewGrpcClient(conn)
	ctx := context.Background()
	_, err = g.RegisterWorkerWithOptions(ctx, fmt.Sprintf("tret-server-%d", os.Getpid()), func(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
		if job != nil && job.Type != nil {
			switch *job.Type {
			case "agent-fail-report":
				return srv.FailReport(ctx, job)
			}
		}
		return srv.Decide(ctx, job)
	},
		zenclient.WithJobType("agent-decide", zenclient.WithLockDuration(5*time.Minute)),
		zenclient.WithJobType("agent-fail-report", zenclient.WithLockDuration(5*time.Minute)),
	)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("server running")
	for {
		time.Sleep(time.Hour)
	}
}
