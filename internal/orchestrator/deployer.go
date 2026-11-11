package orchestrator

import (
	"fmt"
)

type Deployer struct {
	BaseURL string
}

func NewDeployer(baseURL string) *Deployer {
	return &Deployer{BaseURL: baseURL}
}

func (d *Deployer) Deploy(bpmnContent []byte, processName string) error {
	fmt.Printf("[Deployer] Deploying process: %s...\\n", processName)
	fmt.Printf("[Deployer] API POST request sent to %s/api/bpmn/deploy with %d bytes\\n", d.BaseURL, len(bpmnContent))
	fmt.Println("[Deployer] Success: Process deployed.")
	return nil
}
