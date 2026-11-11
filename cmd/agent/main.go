package main

import (
	"context"
	"fmt"
	"os"

	"github.com/pbinitiative/zenbpm-go/internal/api"
	"github.com/pbinitiative/zenbpm-go/internal/orchestrator"
)

func main() {
	fmt.Println("--- ZenBPM Agent Startup ---")
	
	// 1. Initialize Clients (unused for now, but prepared for task polling)
	_ = api.NewClient("http://localhost:8080")
	deployer := orchestrator.NewDeployer("http://localhost:8080")

	// 2. Deployment Step
	fmt.Println("[Startup] Checking BPMN engine status...")
	bpmnFile := "bpmn/agent_loop.bpmn"
	content, err := os.ReadFile(bpmnFile)
	if err != nil {
		fmt.Printf("[Startup] Error reading BPMN file: %v\\n", err)
		os.Exit(1)
	}

	key, err := deployer.Deploy(context.Background(), content, "AgentDecisionLoop")
	_ = key
	if err != nil {
		fmt.Printf("[Startup] Failed to deploy BPMN process: %v\\n", err)
	} else {
		fmt.Println("[Startup] Process deployed successfully.")
	}

	fmt.Println("[Startup] Agent is now listening for tasks from ZenBPM...")
	fmt.Println("--- Entering Listening Mode ---")
}
