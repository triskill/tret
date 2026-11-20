// Package shellexec provides the generic exec-command-v1 job worker: one
// bash -lc command per job with capped output, a bounded timeout, and no
// hidden loop or history. It is a platform tool, not tied to any process.
package shellexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
	"github.com/triskill/tret/pkg/journal"
	"github.com/triskill/tret/pkg/worker"
)

const JobType = "exec-command-v1"

const defaultTimeout = 60 * time.Second

// maxTimeout matches the Runner's hard cap.
const maxTimeout = 120 * time.Second

// execGrace is the time the command may outlive its own timeout before the
// runner's context cancels it. The runner enforces the timeout itself and
// reports it as a timeout; the grace only matters if the runner fails to.
// It covers the escalation of the runner, which takes about 600 ms.
const execGrace = 2 * time.Second

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
type Worker struct {
	Runner *Runner
	guard  *journal.Guard
	// Isolated gives every instance its own directory under the workspace
	// root, so the instances do not write into each other's files. The
	// steps of one instance still share their directory.
	Isolated bool
}

// NewWorker returns a worker which runs commands in the workspace root. The
// guard makes a redelivery of a job repeat its result instead of running the
// command a second time; a worker without one runs the command once per
// delivery.
func NewWorker(workspaceRoot string, guards ...*journal.Guard) (*Worker, error) {
	runner, err := NewRunner(workspaceRoot)
	if err != nil {
		return nil, err
	}
	var guard *journal.Guard
	if len(guards) > 0 {
		guard = guards[0]
	}
	return &Worker{Runner: runner, guard: guard}, nil
}

var _ zenclient.WorkerFunc = (*Worker)(nil).Handle

func invalid(err error) (map[string]any, *zenclient.WorkerError) {
	zero := int32(0)
	return nil, &zenclient.WorkerError{Err: err, ErrorCode: "invalid_input", Retries: &zero}
}

// transient reports a failure which another attempt may survive. It carries
// no error code: the engine then spends one of the job's retries and hands
// the job out again after the backoff of the task definition.
func transient(err error) (map[string]any, *zenclient.WorkerError) {
	return nil, &zenclient.WorkerError{Err: err}
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
	dir, err := w.workspace(job)
	if err != nil {
		return invalid(err)
	}
	// The command is the step. The journal makes a redelivery of the job
	// repeat its result instead of running the command a second time.
	out, err := w.guard.Do(ctx, journal.Key{JobKey: job.GetKey()}, func() (map[string]any, error) {
		// The budget of the command starts with the command, not with the
		// wait for the claim of the step: a context which expires while the
		// worker waits would skip the command and store its cancellation as
		// the result of the step.
		//
		// The command must not die with the job stream either: the engine
		// redelivers the job after a reconnect, and a command killed halfway
		// is a partially applied side effect which then runs a second time.
		runCtx, cancel := worker.Detach(ctx, timeout+execGrace)
		defer cancel()
		result, err := w.Runner.RunIn(runCtx, dir, input.Command, timeout)
		if err != nil {
			// Run errors only before the process starts (size, encoding,
			// validation); they never mean an unknown outcome.
			return nil, err
		}
		duration := int64(result.Duration / time.Millisecond)
		if result.Duration < 0 {
			duration = 0
		}
		return map[string]any{"exec_result": map[string]any{
			"exit_code":   result.ExitCode,
			"stdout":      result.Stdout,
			"stderr":      result.Stderr,
			"timed_out":   result.TimedOut,
			"cancelled":   result.Cancelled,
			"truncated":   result.Truncated,
			"duration_ms": duration,
		}}, nil
	})
	if err != nil {
		if errors.Is(err, journal.ErrHeld) {
			// Another worker is running the command: let the engine hand the
			// job out again instead of running it twice.
			return transient(err)
		}
		return invalid(err)
	}
	return out, nil
}

// workspace returns the directory a command of the job runs in. With the
// isolation on, every instance has its own directory under the workspace
// root; the steps of one instance share it, the instances do not share
// theirs.
func (w *Worker) workspace(job *proto.WaitingJob) (string, error) {
	root := w.Runner.WorkspaceRoot
	if !w.Isolated {
		return root, nil
	}
	key := job.GetInstanceKey()
	if key == 0 {
		// A job without an instance has no directory of its own.
		return root, nil
	}
	dir := filepath.Join(root, "instances", strconv.FormatInt(key, 10))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("workspace of instance %d: %w", key, err)
	}
	return dir, nil
}
