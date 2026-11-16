// Package report is an application-owned worker, not part of tret's LLM primitive.
package report

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
)

// Handle returns a deterministic report. The BPMN gateway selects the queue;
// neither this worker nor the LLM controls process transitions.
func Handle(_ context.Context, job *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
	var input struct {
		Classification struct {
			StructuredOutput struct {
				Approved *bool `json:"approved"`
			} `json:"structured_output"`
		} `json:"classification"`
	}
	if err := json.Unmarshal(job.GetInputVariables(), &input); err != nil {
		return invalidInput(fmt.Errorf("report input: %w", err))
	}
	if input.Classification.StructuredOutput.Approved == nil {
		return invalidInput(fmt.Errorf("report requires classification.structured_output.approved boolean"))
	}
	queue := job.GetHeaders()["queue"]
	if queue == "" {
		return invalidInput(fmt.Errorf("report requires queue header"))
	}
	return map[string]any{
		"report": fmt.Sprintf("Queue: %s\nApproved: %t", queue, *input.Classification.StructuredOutput.Approved),
		"queue":  queue,
	}, nil
}

func invalidInput(err error) (map[string]any, *zenclient.WorkerError) {
	zero := int32(0)
	return nil, &zenclient.WorkerError{Err: err, ErrorCode: "invalid_input", Retries: &zero}
}
