package orchestrator

import (
	"context"

	"github.com/pbinitiative/zenbpm-go/internal/zen"
)

type Deployer struct {
	Client *zen.Client
}

func NewDeployer(baseURL string) *Deployer {
	return &Deployer{Client: zen.NewClient(baseURL)}
}

func (d *Deployer) Deploy(ctx context.Context, bpmnContent []byte, processName string) (string, error) {
	key, _, err := d.Client.Deploy(bpmnContent)
	if err != nil {
		return "", err
	}
	return key, nil
}
