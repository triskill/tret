// Command agent is the workspace-facing CLI: it starts (or attaches to) an
// agent_loop run on the engine and answers the run's agent-reply user tasks
// with human input. The tret server must be running: it owns deployment,
// the decide worker, and durable state.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/triskill/tret/internal/zen"
)

func main() {
	goal := flag.String("goal", "", "goal of a new run (passed as the initial user message)")
	continueKey := flag.String("continue", "", "business key of existing run to attach to")
	engineURL := flag.String("engine", "http://localhost:8080", "engine REST URL")
	workspace := flag.String("workspace", ".", "workspace dir")
	flag.Parse()
	_ = workspace // tool execution happens server-side in the workspace; reserved for render state

	zc := zen.NewClient(*engineURL)
	def, err := zc.GetLatestProcessDefinition("agent_loop")
	if err != nil {
		fmt.Printf("%v — is the tret server running?\n", err)
		os.Exit(1)
	}

	var instanceKey int64
	var bk string
	if *continueKey != "" {
		bk = *continueKey
		inst, err := zc.GetInstanceByBusinessKey(bk)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		instanceKey = inst.Key
		fmt.Printf("attached to instance %d (businessKey=%s)\n", instanceKey, bk)
	} else {
		bk = fmt.Sprintf("run-%d", time.Now().UnixNano())
		instanceKey, err = zc.StartInstance(def.Key, bk, map[string]any{"business_key": bk})
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Printf("started instance %d (businessKey=%s)\n", instanceKey, bk)
	}
	// The process starts on the reply user task, so the goal (if any) is
	// delivered by auto-completing that first task instead of prompting.
	goalPending := *goal != ""

	scanner := bufio.NewScanner(os.Stdin)
	for {
		inst, _ := zc.GetInstance(instanceKey)
		if inst != nil && (inst.State == "completed" || inst.State == "terminated" || inst.State == "failed") {
			fmt.Printf("instance state: %s\n", inst.State)
			break
		}
		jobs, _ := zc.ListJobs(instanceKey)
		handled := false
		for _, j := range jobs {
			if j.State == "active" && j.Type == "agent-reply" {
				if at, ok := j.InputVariables["assistant_text"].(string); ok && at != "" {
					fmt.Printf("Assistant: %s\n", at)
				}
				if goalPending {
					goalPending = false
					fmt.Printf("You> %s\n", *goal)
					if err := zc.CompleteJob(j.Key, map[string]any{"user_message": *goal}); err != nil {
						fmt.Println(err)
					}
					handled = true
					continue
				}
				fmt.Print("You> ")
				if !scanner.Scan() {
					return
				}
				text := strings.TrimSpace(scanner.Text())
				if text == "/exit" {
					_ = zc.CancelInstance(instanceKey)
					fmt.Println("bye")
					return
				}
				if err := zc.CompleteJob(j.Key, map[string]any{"user_message": text}); err != nil {
					fmt.Println(err)
				}
				handled = true
			}
		}
		if !handled {
			time.Sleep(time.Second)
		}
	}
}
