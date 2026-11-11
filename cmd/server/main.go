package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/pbinitiative/zenbpm-go/internal/brain"
	"github.com/pbinitiative/zenbpm-go/internal/config"
	"github.com/pbinitiative/zenbpm-go/internal/contextstore"
	"github.com/pbinitiative/zenbpm-go/internal/executor"
	"github.com/pbinitiative/zenbpm-go/internal/server"
	"github.com/pbinitiative/zenbpm-go/internal/zen"
	"github.com/pbinitiative/zenbpm/pkg/zenclient"
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
		llm = brain.NewOpenAICompat("https://api.openai.com", cfg.Model, cfg.APIKey)
	} else {
		llm = brain.NewOllamaLLM("http://localhost:11434", cfg.Model)
	}
	if os.Getenv("MOCK_LLM") == "1" {
		llm = &brain.FinishLLM{}
	}
	ctxStore := contextstore.New(cfg.StateDir)
	srv := server.New(llm, runner, ctxStore)

	// deploy
	bpmn, _ := os.ReadFile("bpmn/agent_loop.bpmn")
	zc := zen.NewClient(cfg.EngineREST)
	if _, _, err := zc.Deploy(bpmn); err != nil {
		fmt.Printf("deploy warning: %v\n", err)
	}

	conn, err := zen.DialGRPC(cfg.EngineGRPC)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	g := zen.NewGrpcClient(conn)
	ctx := context.Background()
	_, err = g.RegisterWorkerWithOptions(ctx, fmt.Sprintf("zenbpm-go-server-%d", os.Getpid()), func(ctx context.Context, job *zen.WaitingJob) (map[string]any, *zen.WorkerError) {
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
