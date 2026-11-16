// Package worker provides explicit dispatch for ZenBPM job workers.
package worker

import (
	"context"
	"fmt"
	"sort"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
)

// Registry is configured before registration and is read-only while serving jobs.
type Registry struct {
	handlers map[string]zenclient.WorkerFunc
}

func NewRegistry() *Registry { return &Registry{handlers: make(map[string]zenclient.WorkerFunc)} }

func (r *Registry) Register(jobType string, handler zenclient.WorkerFunc) error {
	if jobType == "" || handler == nil {
		return fmt.Errorf("job type and handler are required")
	}
	if _, exists := r.handlers[jobType]; exists {
		return fmt.Errorf("duplicate job type %q", jobType)
	}
	r.handlers[jobType] = handler
	return nil
}

func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

func (r *Registry) Handle(ctx context.Context, job *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
	if job != nil {
		if handler, ok := r.handlers[job.GetType()]; ok {
			return handler(ctx, job)
		}
	}
	zero := int32(0)
	return nil, &zenclient.WorkerError{Err: fmt.Errorf("unregistered job type %q", job.GetType()), ErrorCode: "unknown_job_type", Retries: &zero}
}
