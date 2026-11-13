package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/triskill/tret/internal/zen"
)

// runAgent is the workspace-facing CLI: it starts (or attaches to) an
// agent_loop run on the engine and answers the run's agent-reply user tasks
// with human input. The tret server must be running: it owns deployment,
// the decide worker, and durable state.
func runAgent(args []string) {
	if err := executeAgent(args, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeAgent(args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	goal := fs.String("goal", "", "goal of a new run (passed as the initial user message)")
	continueKey := fs.String("continue", "", "business key of existing run to attach to")
	engineURL := fs.String("engine", "http://localhost:8080", "engine REST URL")
	workspace := fs.String("workspace", ".", "workspace dir")
	fs.Parse(args)
	_ = workspace // tool execution happens server-side in the workspace; reserved for render state

	zc := zen.NewClient(*engineURL)
	def, err := zc.GetLatestProcessDefinition("agent_loop")
	if err != nil {
		return fmt.Errorf("%w — is the tret server running?", err)
	}

	var instanceKey int64
	var bk string
	if *continueKey != "" {
		bk = *continueKey
		inst, err := zc.GetInstanceByBusinessKey(bk)
		if err != nil {
			return err
		}
		instanceKey = inst.Key
		fmt.Fprintf(output, "attached to instance %d (businessKey=%s)\n", instanceKey, bk)
	} else {
		bk = fmt.Sprintf("run-%d", time.Now().UnixNano())
		instanceKey, err = zc.StartInstance(def.Key, bk, map[string]any{"business_key": bk, "interactive": true})
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "started instance %d (businessKey=%s)\n", instanceKey, bk)
	}
	// The process starts on the reply user task, so the goal (if any) is
	// delivered by auto-completing that first task instead of prompting.
	goalPending := *goal != ""

	scanner := bufio.NewScanner(input)
	completedReplies := make(map[int64]bool)
	for {
		inst, err := zc.GetInstance(instanceKey)
		if err != nil {
			return fmt.Errorf("poll instance %d: %w", instanceKey, err)
		}
		if done, err := reportAgentResult(output, inst); done {
			return err
		}
		incidents, err := zc.ListIncidents(instanceKey)
		if err != nil {
			return fmt.Errorf("poll incidents for instance %d: %w", instanceKey, err)
		}
		for _, incident := range incidents {
			if incident.State != "resolved" {
				return fmt.Errorf("instance %d incident %d: %s", instanceKey, incident.Key, incident.Message)
			}
		}
		jobs, err := zc.ListJobs(instanceKey)
		if err != nil {
			return fmt.Errorf("poll jobs for instance %d: %w", instanceKey, err)
		}
		handled := false
		for _, j := range jobs {
			if j.State == "active" && j.Type == "agent-reply" && !completedReplies[j.Key] {
				if at, ok := j.InputVariables["assistant_text"].(string); ok && at != "" {
					fmt.Fprintf(output, "Assistant: %s\n", at)
				}
				if goalPending {
					goalPending = false
					fmt.Fprintf(output, "You> %s\n", *goal)
					if err := zc.CompleteJob(j.Key, map[string]any{"user_message": *goal}); err != nil {
						return fmt.Errorf("complete reply job %d: %w", j.Key, err)
					}
					completedReplies[j.Key] = true
					handled = true
					continue
				}
				fmt.Fprint(output, "You> ")
				if !scanner.Scan() {
					return scanner.Err()
				}
				text := strings.TrimSpace(scanner.Text())
				if text == "/exit" {
					if err := zc.CancelInstance(instanceKey); err != nil {
						return fmt.Errorf("cancel instance %d: %w", instanceKey, err)
					}
					fmt.Fprintln(output, "bye")
					return nil
				}
				if err := zc.CompleteJob(j.Key, map[string]any{"user_message": text}); err != nil {
					return fmt.Errorf("complete reply job %d: %w", j.Key, err)
				}
				completedReplies[j.Key] = true
				handled = true
			}
		}
		if !handled {
			time.Sleep(time.Second)
		}
	}
}

// Engine completion alone does not imply that the agent achieved its goal.
func reportAgentResult(output io.Writer, inst *zen.Instance) (bool, error) {
	if inst.State != "completed" && inst.State != "terminated" && inst.State != "failed" {
		return false, nil
	}
	if text, _ := inst.Variables["assistant_text"].(string); text != "" {
		fmt.Fprintf(output, "Assistant: %s\n", text)
	}
	fmt.Fprintf(output, "instance state: %s\n", inst.State)
	outcome, _ := inst.Variables["run_outcome"].(string)
	message, _ := inst.Variables["error_message"].(string)
	if message != "" {
		return true, fmt.Errorf("run failed: %s", message)
	}
	if inst.State != "completed" || outcome == "failure" {
		return true, fmt.Errorf("run failed (instance state: %s, run_outcome: %q)", inst.State, outcome)
	}
	if outcome == "success" {
		fmt.Fprintln(output, "run outcome: success")
	}
	return true, nil
}
