// Package worker provides explicit dispatch for ZenBPM job workers, and the
// helpers a worker needs to serve jobs safely: a subscription whose lock
// outlasts the work, and a context which survives a broken job stream.
package worker

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
)

// DefaultLockDuration is the lock a subscription asks for when nothing else
// names one. It is longer than the longest runtime of the built-in workers,
// because a lock which lapses while the worker is still busy hands the job to
// a second worker: the side effect then runs twice, concurrently.
const DefaultLockDuration = 5 * time.Minute

// Detach returns a context for a side effect which must survive a broken job
// stream. The engine cancels the job context when the stream to the worker
// breaks — a reconnect after a network blip or an engine restart — and the
// work in flight would be aborted halfway: its side effect applied, its
// result thrown away, the job handed out again. The detached context keeps
// the values of the job context, ignores its cancellation, and carries its
// own deadline.
func Detach(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), budget)
}

// Spec describes how a worker subscribes to a job type.
type Spec struct {
	// JobType is the engine job type the handler serves.
	JobType string
	// LockDuration is how long the engine reserves a delivered job for this
	// worker. Zero means DefaultLockDuration.
	LockDuration time.Duration
	// MaxActiveJobs caps the jobs of this type the worker holds at once.
	// Zero means the engine's default.
	MaxActiveJobs int
	// MaxRuntime is the longest the handler may run. The lock must outlast
	// it, or the engine hands the job to another worker while this one is
	// still working on it. Zero means the handler declares no runtime.
	MaxRuntime time.Duration
}

// Registry is configured before registration and is read-only while serving jobs.
type Registry struct {
	handlers map[string]zenclient.WorkerFunc
	specs    map[string]Spec
	inflight sync.WaitGroup
}

func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]zenclient.WorkerFunc), specs: make(map[string]Spec)}
}

// Register adds a handler for one job type. The spec must be consistent with
// the handler: a lock which does not outlast the handler's runtime is
// refused, because the engine would then deliver the same job to a second
// worker while the first is still working on it.
func (r *Registry) Register(spec Spec, handler zenclient.WorkerFunc) error {
	if spec.JobType == "" || handler == nil {
		return fmt.Errorf("job type and handler are required")
	}
	if _, exists := r.handlers[spec.JobType]; exists {
		return fmt.Errorf("duplicate job type %q", spec.JobType)
	}
	if spec.LockDuration == 0 {
		spec.LockDuration = DefaultLockDuration
	}
	if spec.LockDuration <= 0 {
		return fmt.Errorf("job type %q: lock duration must be positive", spec.JobType)
	}
	if spec.MaxRuntime > 0 && spec.LockDuration <= spec.MaxRuntime {
		return fmt.Errorf("job type %q: lock duration %s must outlast the worker runtime %s", spec.JobType, spec.LockDuration, spec.MaxRuntime)
	}
	if spec.MaxActiveJobs < 0 {
		return fmt.Errorf("job type %q: max active jobs must not be negative", spec.JobType)
	}
	r.handlers[spec.JobType] = handler
	r.specs[spec.JobType] = spec
	return nil
}

// Types lists the job types the registry serves.
func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// Options turns the registered specs into engine subscription options.
func (r *Registry) Options() []zenclient.WorkerOption {
	options := make([]zenclient.WorkerOption, 0, len(r.specs))
	for _, typ := range r.Types() {
		spec := r.specs[typ]
		settings := []zenclient.SubscriptionOption{zenclient.WithLockDuration(spec.LockDuration)}
		if spec.MaxActiveJobs > 0 {
			settings = append(settings, zenclient.WithMaxActiveJobs(spec.MaxActiveJobs))
		}
		options = append(options, zenclient.WithJobType(typ, settings...))
	}
	return options
}

// Handle runs the handler of the job's type. The job is counted as in flight
// until the handler returns, so a shutdown can wait for the work to finish
// instead of cutting it short.
func (r *Registry) Handle(ctx context.Context, job *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
	if job != nil {
		if handler, ok := r.handlers[job.GetType()]; ok {
			r.inflight.Add(1)
			defer r.inflight.Done()
			return handler(ctx, job)
		}
	}
	zero := int32(0)
	return nil, &zenclient.WorkerError{Err: fmt.Errorf("unregistered job type %q", job.GetType()), ErrorCode: "unknown_job_type", Retries: &zero}
}

// Drain waits until no job is in flight, or until ctx is done. A server
// which stops accepting jobs and then drains does not cut a running side
// effect short; the engine redelivers the job if the worker dies before it
// reports the result.
func (r *Registry) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		r.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Subscribe registers a handler for the given job types with the default
// lock duration. Third-party workers should use it (or name their own lock)
// rather than zenclient.RegisterWorker, whose engine default of 30 s is
// shorter than many jobs take.
func Subscribe(ctx context.Context, g *zenclient.Grpc, clientID string, f zenclient.WorkerFunc, jobTypes ...string) (*zenclient.Worker, error) {
	specs := make([]Spec, 0, len(jobTypes))
	for _, jobType := range jobTypes {
		specs = append(specs, Spec{JobType: jobType})
	}
	return g.RegisterWorkerWithOptions(ctx, clientID, f, Options(specs...)...)
}

// Options turns specs into engine subscription options.
func Options(specs ...Spec) []zenclient.WorkerOption {
	options := make([]zenclient.WorkerOption, 0, len(specs))
	for _, spec := range specs {
		lock := spec.LockDuration
		if lock == 0 {
			lock = DefaultLockDuration
		}
		settings := []zenclient.SubscriptionOption{zenclient.WithLockDuration(lock)}
		if spec.MaxActiveJobs > 0 {
			settings = append(settings, zenclient.WithMaxActiveJobs(spec.MaxActiveJobs))
		}
		options = append(options, zenclient.WithJobType(spec.JobType, settings...))
	}
	return options
}
