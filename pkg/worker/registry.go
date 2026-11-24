// Package worker provides explicit dispatch for ZenBPM job workers, and the
// helpers a worker needs to serve jobs safely: a subscription whose lock
// outlasts the work, and a context which survives a broken job stream.
package worker

import (
	"context"
	"fmt"
	"log/slog"
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

// Registry is configured before serving jobs and can be shut down with Drain.
type Registry struct {
	handlers map[string]zenclient.WorkerFunc
	specs    map[string]Spec

	renewer renewerBox

	intake   sync.Mutex
	draining bool
	inflight sync.WaitGroup
}

func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]zenclient.WorkerFunc), specs: make(map[string]Spec)}
}

// SetRenewer gives the registry the means to renew the lock of a delivered
// job while the job runs. The zenclient's worker implements the interface;
// the registration hands it out once the stream is open.
func (r *Registry) SetRenewer(renewer LockRenewer) {
	r.renewer.set(renewer)
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
// instead of cutting it short, and its outcome is logged with the fields
// which identify it.
func (r *Registry) Handle(ctx context.Context, job *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
	if job != nil {
		if handler, ok := r.handlers[job.GetType()]; ok {
			r.intake.Lock()
			if r.draining {
				r.intake.Unlock()
				return nil, &zenclient.WorkerError{Err: fmt.Errorf("worker is shutting down")}
			}
			r.inflight.Add(1)
			r.intake.Unlock()
			defer r.inflight.Done()
			return r.run(ctx, job, handler)
		}
	}
	zero := int32(0)
	return nil, &zenclient.WorkerError{Err: fmt.Errorf("unregistered job type %q", job.GetType()), ErrorCode: "unknown_job_type", Retries: &zero}
}

// run calls the handler and logs the outcome of the job. The lock of the
// job is renewed while it runs: a lock which lapses before the job is done
// hands the job to a second worker, and the side effect runs twice.
func (r *Registry) run(ctx context.Context, job *proto.WaitingJob, handler zenclient.WorkerFunc) (map[string]any, *zenclient.WorkerError) {
	stopRenewal := r.renewLock(ctx, job)
	defer stopRenewal()
	started := time.Now()
	result, workerErr := handler(ctx, job)
	fields := []any{
		"job_key", job.GetKey(),
		"instance_key", job.GetInstanceKey(),
		"element_id", job.GetElementId(),
		"job_type", job.GetType(),
		"attempt", job.GetAttempt(),
		"delivery_token", job.GetDeliveryToken(),
		"duration_ms", time.Since(started).Milliseconds(),
	}
	if workerErr != nil {
		slog.Warn("job failed", append(fields, "outcome", "failed", "error_code", workerErr.ErrorCode, "err", workerErr.Err)...)
		return result, workerErr
	}
	slog.Info("job completed", append(fields, "outcome", "completed")...)
	return result, nil
}

// StopAccepting closes intake without waiting for admitted handlers.
func (r *Registry) StopAccepting() {
	r.intake.Lock()
	r.draining = true
	r.intake.Unlock()
}

// Drain closes intake and waits until all admitted handlers return or ctx is
// done. Callers should stop engine subscriptions before draining.
func (r *Registry) Drain(ctx context.Context) error {
	r.StopAccepting()
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
// shorter than many jobs take. The lock of a delivered job is renewed while
// the job runs.
func Subscribe(ctx context.Context, g *zenclient.Grpc, clientID string, f zenclient.WorkerFunc, jobTypes ...string) (*zenclient.Worker, error) {
	registry := NewRegistry()
	for _, jobType := range jobTypes {
		if err := registry.Register(Spec{JobType: jobType}, f); err != nil {
			return nil, err
		}
	}
	handle, err := g.RegisterWorkerWithOptions(ctx, clientID, registry.Handle, registry.Options()...)
	if err != nil {
		return nil, err
	}
	registry.SetRenewer(handle)
	return handle, nil
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
