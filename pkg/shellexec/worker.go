// Package shellexec provides the generic exec-command-v1 job worker: one
// bash -lc command per job with capped output, a bounded timeout, and no
// hidden loop or history. It is a platform tool, not tied to any process.
package shellexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
	"github.com/triskill/tret/pkg/worker"
)

const JobType = "exec-command-v1"

const defaultTimeout = 60 * time.Second

// maxTimeout matches the Runner's hard cap.
const maxTimeout = 120 * time.Second

// execGrace is the time the command may outlive its own timeout before the
// runner's context cancels it. The runner enforces the timeout itself and
// reports it as a timeout; the grace only matters if the runner fails to.
const execGrace = 5 * time.Second

// MaxTimeout is the longest a command may run. It is also the worker's
// longest runtime, which the job lock must outlast.
const MaxTimeout = maxTimeout

// Request is the exec_request job variable.
type Request struct {
	// Command is the bash script to run; required and non-empty.
	Command string `json:"command"`
	// TimeoutSec is optional, in (0, 120]; defaults to 60.
	TimeoutSec *float64 `json:"timeout_sec,omitempty"`
}

// Worker executes jobs in the server-owned workspace root. Commands run
// with the server's permissions; there is no sandbox. The engine may
// redeliver a job, so treat commands as at-least-once.
type Worker struct{ Runner *Runner }

func NewWorker(workspaceRoot string) (*Worker, error) {
	runner, err := NewRunner(workspaceRoot)
	if err != nil {
		return nil, err
	}
	return &Worker{Runner: runner}, nil
}

var _ zenclient.WorkerFunc = (*Worker)(nil).Handle

func invalid(err error) (map[string]any, *zenclient.WorkerError) {
	zero := int32(0)
	return nil, &zenclient.WorkerError{Err: err, ErrorCode: "invalid_input", Retries: &zero}
}

func strictDecode(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

// Handle validates exec_request, runs one command, and publishes exec_result.
// Command outcomes (non-zero exit, timeout) are ordinary results, not worker
// errors; only request validation problems stop the job.
func (w *Worker) Handle(ctx context.Context, job *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(job.GetInputVariables(), &envelope); err != nil {
		return invalid(err)
	}
	raw, ok := envelope["exec_request"]
	if !ok {
		return invalid(fmt.Errorf("exec_request is required"))
	}
	var input Request
	if err := strictDecode(raw, &input); err != nil {
		return invalid(err)
	}
	if input.Command == "" || containsNUL(input.Command) {
		return invalid(fmt.Errorf("exec_request.command must be a non-empty string without NUL"))
	}
	timeout := defaultTimeout
	if input.TimeoutSec != nil {
		if *input.TimeoutSec <= 0 || *input.TimeoutSec > float64(maxTimeout/time.Second) {
			return invalid(fmt.Errorf("timeout_sec must be in (0, %d]", int(maxTimeout/time.Second)))
		}
		timeout = time.Duration(*input.TimeoutSec * float64(time.Second))
	}
	// The command must not die with the job stream: the engine redelivers
	// the job after a reconnect, and a command killed halfway is a partially
	// applied side effect which then runs a second time.
	runCtx, cancel := worker.Detach(ctx, timeout+execGrace)
	defer cancel()
	result, err := w.Runner.Run(runCtx, input.Command, timeout)
	if err != nil {
		// Run errors only before the process starts (size, encoding,
		// validation); they never mean an unknown outcome.
		return invalid(err)
	}
	duration := int64(result.Duration / time.Millisecond)
	if result.Duration < 0 {
		duration = 0
	}
	out := map[string]any{"exec_result": map[string]any{
		"exit_code":   result.ExitCode,
		"stdout":      result.Stdout,
		"stderr":      result.Stderr,
		"timed_out":   result.TimedOut,
		"cancelled":   result.Cancelled,
		"truncated":   result.Truncated,
		"duration_ms": duration,
	}}
	return out, nil
}
