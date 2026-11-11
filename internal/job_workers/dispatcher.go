package job_workers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/pbinitiative/zenbpm-go/internal/brain"
)

type JobRequest struct {
	TaskID  string
	Payload string
}

type Decision struct {
	Route   string `json:"route"`
	Command string `json:"command"`
}

type Dispatcher struct {
	Shell *ShellWorker
	User  *UserTaskWorker
	LLM   brain.LLMProvider
}

func NewDispatcher(shell *ShellWorker, user *UserTaskWorker, llm brain.LLMProvider) *Dispatcher {
	return &Dispatcher{
		Shell: shell,
		User:  user,
		LLM:   llm,
	}
}

func (d *Dispatcher) Dispatch(ctx context.Context, req JobRequest) error {
	fmt.Printf("[Dispatcher] Analyzing Task %s: %s\\n", req.TaskID, req.Payload)

	prompt := fmt.Sprintf("Classify this request as 'tool' or 'message'. Request: %s", req.Payload)
	rawDecision, err := d.LLM.Complete([]string{prompt})
	if err != nil {
		return fmt.Errorf("LLM decision failed: %w", err)
	}

	var decision Decision
	err = json.Unmarshal([]byte(rawDecision), &decision)
	if err != nil {
		fmt.Printf("[Dispatcher] JSON Parse Error: %v. Raw: %s\\n", err, rawDecision)
		return err
	}

	fmt.Printf("[Dispatcher] Decision Routing: %s\\n", decision.Route)

	// STRICT ROUTING based on the parsed field
	if decision.Route == "tool" {
		fmt.Println("[Dispatcher] Routing to ShellWorker (serviceTask)...")
		res, err := d.Shell.Handle(ctx, decision.Command)
		if err != nil {
			return fmt.Errorf("shell worker error: %w", err)
		}
		fmt.Printf("[Dispatcher] ShellWorker finished successfully. Result length: %d\\n", len(res))
	} else if decision.Route == "message" {
		fmt.Println("[Dispatcher] Routing to UserTaskWorker (userTask)...")
		_, err := d.User.Handle()
		if err != nil {
			return fmt.Errorf("user task error: %w", err)
		}
		fmt.Println("[Dispatcher] UserTaskWorker completed successfully.")
	} else {
		return fmt.Errorf("unknown route type: %s", decision.Route)
	}

	return nil
}
