package job_workers

import (
	"context"
	"time"
	"fmt"
	"github.com/pbinitiative/zenbpm-go/internal/executor"
)

type ShellWorker struct {
	Runner *executor.Runner
}

func NewShellWorker(runner *executor.Runner) *ShellWorker {
	return &ShellWorker{Runner: runner}
}

func (w *ShellWorker) Handle(ctx context.Context, command string) (string, error) {
	fmt.Printf("[ShellWorker] Executing serviceTask: %s\n", command)
	res, err := w.Runner.Run(ctx, command, 60*time.Second)
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}
