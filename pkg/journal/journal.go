// Package journal remembers the outcome of a step, so that a job the engine
// hands out again repeats its result instead of its work.
//
// The engine reserves a delivered job for a worker until the lock lapses and
// then hands it out again; a broken job stream does the same. A completion is
// not bound to a delivery, so the first completion ends the job and the side
// effect of every other delivery happened for nothing. The journal closes
// that gap: the step is keyed on the engine job key, the result is stored
// next to it, and a redelivery returns the stored result.
//
// The key is the job key alone. The attempt and the delivery token change
// with every delivery, so an effect keyed on them would run again on every
// retry. A business key — a key of the operation the step performs, not of
// the step — can be added to Key when the engine exposes one to the worker.
package journal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrHeld reports that another owner is running the step. The caller should
// fail the job so the engine hands it out again later.
var ErrHeld = errors.New("the step is running elsewhere")

// Key identifies a step.
type Key struct {
	// JobKey is the engine key of the job. It is stable across the
	// deliveries and the attempts of one job.
	JobKey int64
	// Business is an optional key of the business operation the step
	// performs. Two jobs of the same operation share it.
	Business string
}

func (k Key) String() string {
	if k.Business == "" {
		return fmt.Sprintf("job-%d", k.JobKey)
	}
	return fmt.Sprintf("job-%d-%s", k.JobKey, k.Business)
}

// Journal stores the outcome of steps.
//
// Implementations must be safe for use by several processes at once: two
// workers may hold the same job at the same time, and only one of them may
// run the step.
type Journal interface {
	// Result returns the stored result of a step which already succeeded.
	// The second return value reports whether such a result exists.
	Result(ctx context.Context, key Key) (map[string]any, bool, error)
	// Acquire claims a step for the owner until the lease expires. It
	// reports false when another owner holds the claim.
	Acquire(ctx context.Context, key Key, owner string, lease time.Duration) (bool, error)
	// Release drops the claim of the owner.
	Release(ctx context.Context, key Key, owner string) error
	// Store saves the result of a step.
	Store(ctx context.Context, key Key, owner string, result map[string]any) error
}

// DefaultLease is how long a claim on a step lasts. A worker which dies
// while it holds the step releases it after this time.
const DefaultLease = 5 * time.Minute

// Guard applies the journal protocol to the work of a worker.
//
// A worker which has no journal runs its work exactly once per delivery, the
// way it did before: the journal is what makes a redelivery a repeat.
type Guard struct {
	journal Journal
	owner   string
	lease   time.Duration
}

// NewGuard returns a guard for the journal. An owner names the worker
// process; it must be unique per process, or two workers would share a claim.
func NewGuard(journal Journal, owner string, lease time.Duration) *Guard {
	if lease <= 0 {
		lease = DefaultLease
	}
	return &Guard{journal: journal, owner: owner, lease: lease}
}

// Owner names the worker process behind the guard.
func (g *Guard) Owner() string {
	if g == nil {
		return ""
	}
	return g.owner
}

// Do runs the work of a step once. It returns the stored result when the
// step already succeeded, ErrHeld when another owner is running it, and the
// error of the work itself when the work failed.
//
// A journal which cannot be reached or read does not stop the work: the step
// runs as it did before the journal existed, and the failure is logged. A
// journal which cannot be written leaves the work unrepeated only until the
// next delivery.
func (g *Guard) Do(ctx context.Context, key Key, work func() (map[string]any, error)) (map[string]any, error) {
	if g == nil || g.journal == nil {
		return work()
	}
	if stored, ok, err := g.journal.Result(ctx, key); err != nil {
		slog.Warn("journal: the stored result of the step could not be read", "key", key.String(), "err", err)
	} else if ok {
		slog.Info("journal: the step already succeeded, repeating its result", "key", key.String())
		return stored, nil
	}
	claimed, err := g.journal.Acquire(ctx, key, g.owner, g.lease)
	if err != nil {
		slog.Warn("journal: the step could not be claimed, running it anyway", "key", key.String(), "err", err)
		return work()
	}
	if !claimed {
		return nil, ErrHeld
	}
	defer func() {
		if err := g.journal.Release(ctx, key, g.owner); err != nil {
			slog.Warn("journal: the claim on the step could not be released", "key", key.String(), "err", err)
		}
	}()
	result, err := work()
	if err != nil {
		return nil, err
	}
	if err := g.journal.Store(ctx, key, g.owner, result); err != nil {
		slog.Error("journal: the result of the step could not be stored, a redelivery may repeat it", "key", key.String(), "err", err)
	}
	return result, nil
}
