package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pbinitiative/zenbpm-go/internal/orchestrator"
	"github.com/pbinitiative/zenbpm-go/internal/zen"
)

func main() {
	goal := flag.String("goal", "default goal", "goal")
	continueKey := flag.String("continue", "", "business key of existing run to attach to")
	workspace := flag.String("workspace", ".", "workspace dir")
	serverURL := flag.String("server", "http://localhost:8080", "server url")
	flag.Parse()
	_ = goal

	scanner := bufio.NewScanner(os.Stdin)
	bpmn, err := os.ReadFile("bpmn/agent_loop.bpmn")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	deployer := orchestrator.NewDeployer(*serverURL)
	key, err := deployer.Deploy(context.Background(), bpmn, "agent_loop")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	zc := zen.NewClient(*serverURL)

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
		instanceKey, err = zc.StartInstance(key, bk, map[string]any{})
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Printf("started instance %d (businessKey=%s)\n", instanceKey, bk)
	}
	_ = workspace

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
				} else {
					// no assistant text yet
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
