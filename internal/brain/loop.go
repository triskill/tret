package brain

import (
	"context"
	"fmt"
	"github.com/pbinitiative/zenbpm-go/internal/api"
	"github.com/pbinitiative/zenbpm-go/internal/executor"
	"time"
)

type AgentLoop struct {
	API    *api.Client
	Runner *executor.Runner
	TaskID string
}

func NewAgentLoop(apiClient *api.Client, runner *executor.Runner, taskID string) *AgentLoop {
	return &AgentLoop{
		API:    apiClient,
		Runner: runner,
		TaskID: taskID,
	}
}

func (l *AgentLoop) Run(ctx context.Context) error {
	fmt.Printf("[Brain] Starting loop for Task: %s\n", l.TaskID)

	// 1. Observe
	instr, err := l.API.FetchInstruction(l.TaskID)
	if err != nil {
		return fmt.Errorf("failed to fetch instruction: %w", err)
	}
	fmt.Printf("[Brain] Received Instruction: %s\n", instr.Command)

	// 2. Act (if ToolCall)
	var report api.Report
	if instr.IsToolCall {
		fmt.Println("[Brain] Executing tool call...")
		res, err := l.Runner.Run(ctx, instr.Command, 60*time.Second)
		if err != nil {
			report = api.Report{
				TaskID: l.TaskID,
				Status: "failed",
				Error:  err.Error(),
			}
		} else {
			report = api.Report{
				TaskID: l.TaskID,
				Status: "completed",
				Output: res.Stdout,
			}
		}
	}

	// 3. Report
	err = l.API.ReportResult(report)
	if err != nil {
		return fmt.Errorf("failed to report result: %w", err)
	}

	fmt.Println("[Brain] Loop iteration finished.")
	return nil
}
