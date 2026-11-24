package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
)

// LockRenewer extends the lock the engine holds on a delivered job. The
// zenclient's worker implements it: ExtendDeliveryLock asks the engine to
// move the deadline of the lock of this delivery.
type LockRenewer interface {
	ExtendDeliveryLock(ctx context.Context, job *proto.WaitingJob, d time.Duration) (time.Time, error)
}

const (
	// renewalGrace is how long the registry waits for a renewal to end when
	// the job which asked for it is done. A renewal which is stuck must not
	// hold the completion of the job.
	renewalGrace = 100 * time.Millisecond
	// renewalCallTimeout bounds one renewal. The engine answers a lock
	// extension over the job stream; a stream which is gone must not hold
	// the renewal forever.
	renewalCallTimeout = 10 * time.Second
)

// renewerBox holds the renewer of a registry. The renewer is the
// zenclient's worker, which exists only after the registration returned: the
// engine may hand out a job before that, so the registry takes the renewer
// later and reads it when a job starts.
type renewerBox struct {
	mu      sync.RWMutex
	renewer LockRenewer
}

func (b *renewerBox) set(renewer LockRenewer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.renewer = renewer
}

func (b *renewerBox) get() LockRenewer {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.renewer
}

// renewLock keeps the lock of a delivered job alive while the handler runs.
// A lock which lapses before the job is done hands the job to a second
// worker, and the side effect runs twice. The returned function stops the
// renewal.
func (r *Registry) renewLock(ctx context.Context, job *proto.WaitingJob) func() {
	renewer := r.renewer.get()
	if renewer == nil {
		return func() {}
	}
	spec, known := r.specs[job.GetType()]
	if !known || spec.LockDuration <= 0 {
		return func() {}
	}
	// The renewal outlives the job stream: the engine cancels the context of
	// a delivery when the stream breaks, and the side effect may well be
	// running on.
	renewCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		renewUntil(renewCtx, renewer, job, spec.LockDuration, renewalCallTimeout)
	}()
	return func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(renewalGrace):
		}
	}
}

// renewUntil renews the lock of a job until the context is done. A renewal
// which the engine refuses ends the renewal of that job: the lock is gone,
// and the job is in the hands of another worker.
func renewUntil(ctx context.Context, renewer LockRenewer, job *proto.WaitingJob, lock, callTimeout time.Duration) {
	lockUntil := deadlineOf(job)
	// The engine counts the lock on the clock of the partition leader. When
	// that clock and ours disagree, the deadline of the engine is useless
	// and the lock duration is counted from the moment of receipt instead.
	skewed := false
	for {
		delay := renewalDelay(time.Now(), lockUntil, lock, skewed)
		if delay > 0 && !sleep(ctx, delay) {
			return
		}
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		until, err := renewer.ExtendDeliveryLock(callCtx, job, lock)
		abandoned := callCtx.Err() != nil
		cancel()
		if err != nil {
			if abandoned {
				// The call was given up on. The zenclient keeps the waiter
				// of an abandoned call, and the answer of the engine would
				// go to it instead of to the next renewal of the job, so the
				// renewal of this job ends here.
				slog.Warn("the renewal of the lock was given up on", "job_key", job.GetKey(), "err", err)
				return
			}
			if errors.Is(err, zenclient.ErrLockNotHeld) || errors.Is(err, zenclient.ErrLockHeldByOtherClient) {
				// The lock is gone: the engine handed the job to another
				// worker, or completed it elsewhere.
				slog.Warn("the lock of the job is gone", "job_key", job.GetKey(), "err", err)
				return
			}
			slog.Warn("the lock of the job was not renewed", "job_key", job.GetKey(), "err", err)
			// The engine may be busy or the stream may be gone: the job is
			// still running, so try again in a moment.
			if !sleep(ctx, lock/4) {
				return
			}
			continue
		}
		if until.IsZero() {
			until = time.Now().Add(lock)
		}
		lockUntil = until
		if !until.After(time.Now()) {
			// The deadline of the engine is already past: its clock and ours
			// do not agree, so the local schedule takes over from here.
			skewed = true
		}
		slog.Debug("the lock of the job was renewed", "job_key", job.GetKey(), "lock_until", lockUntil)
	}
}

// deadlineOf is the moment the lock of a delivery lapses, on the clock of the
// engine. The zero time means the engine named no deadline.
func deadlineOf(job *proto.WaitingJob) time.Time {
	millis := job.GetLockUntil()
	if millis == 0 {
		return time.Time{}
	}
	return time.UnixMilli(millis)
}

// renewalDelay returns how long to wait before the lock of a delivered job is
// renewed. The local schedule counts half of the lock duration from the
// moment of receipt; the deadline of the engine, when the clocks agree, may
// make the renewal come earlier.
func renewalDelay(now time.Time, lockUntil time.Time, lock time.Duration, skewed bool) time.Duration {
	delay := lock / 2
	if skewed || lockUntil.IsZero() {
		return delay
	}
	if !lockUntil.After(now) {
		// The deadline passed: the lock is gone, ask for a new one now.
		return 0
	}
	if early := lockUntil.Sub(now) - lock/4; early < delay {
		delay = early
	}
	if delay < 0 {
		delay = 0
	}
	return delay
}

// sleep waits for the duration and reports whether the wait ended on time.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
