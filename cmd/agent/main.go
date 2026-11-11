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
	serverURL := flag.String("server", "http://localhost:8080", "server url")
	flag.Parse()

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
	instanceKey, err := zc.StartInstance(key, "run-1", map[string]any{"goal": *goal, "route": ""})
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Printf("started instance %d\n", instanceKey)

	scanner := bufio.NewScanner(os.Stdin)
	for {
		inst, _ := zc.GetInstanceByBusinessKey("run-1")
		if inst != nil && (inst.State == "completed" || inst.State == "terminated" || inst.State == "failed") {
			fmt.Printf("instance state: %s\n", inst.State)
			break
		}
		jobs, _ := zc.ListJobs(instanceKey)
		handled := false
		for _, j := range jobs {
			if j.State == "active" && j.Type == "agent-reply" {
				fmt.Print("human input> ")
				if !scanner.Scan() {
					return
				}
				text := strings.TrimSpace(scanner.Text())
				if err := zc.CompleteJob(j.Key, map[string]any{"message": text, "route": "message"}); err != nil {
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
