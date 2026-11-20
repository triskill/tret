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

// ClaimInfo describes the claim on a step.
type ClaimInfo struct {
	// Owner is the worker which holds the claim.
	Owner string
	// Until is the moment the claim expires. A worker which finds the claim
	// past that moment may take the step over: the holder is presumed gone.
	Until time.Time
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
	// reports false when another owner holds the claim. A live claim is
	// refused whoever holds it: the same worker may hold the step in
	// another delivery of the same job.
	Acquire(ctx context.Context, key Key, owner string, lease time.Duration) (bool, error)
	// Claim returns the claim on a step, if there is one.
	Claim(ctx context.Context, key Key) (ClaimInfo, bool, error)
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
// step already succeeded, ErrHeld when the step could not be claimed at all,
// and the error of the work itself when the work failed.
//
// A journal which cannot be reached or read does not stop the work: the step
// runs as it did before the journal existed, and the failure is logged. A
// journal which cannot be written leaves the work unrepeated only until the
// next delivery.
func (g *Guard) Do(ctx context.Context, key Key, work func() (map[string]any, error)) (map[string]any, error) {
	if g == nil || g.journal == nil {
		return work()
	}
	// The journal outlives the delivery, so its calls are not bound to the
	// life of the job stream: a result which is stored must be stored even
	// when the stream broke in the meantime.
	store := context.WithoutCancel(ctx)
	if stored, ok, err := g.journal.Result(store, key); err != nil {
		slog.Warn("journal: the stored result of the step could not be read", "key", key.String(), "err", err)
	} else if ok {
		slog.Info("journal: the step already succeeded, repeating its result", "key", key.String())
		return stored, nil
	}
	claimed, err := g.journal.Acquire(store, key, g.owner, g.lease)
	if err != nil {
		slog.Warn("journal: the step could not be claimed, running it anyway", "key", key.String(), "err", err)
		return work()
	}
	for !claimed {
		// Another worker is running the step. Its result is worth waiting
		// for: the work is bounded, and failing here would spend a retry of
		// the job for a step which is about to finish. The engine spends no
		// retry for a redelivery, but a failure is a failure — and with the
		// default retry budget it ends the instance with an incident.
		stored, ok := g.waitForResult(ctx, key)
		if ok {
			slog.Info("journal: the step finished elsewhere, repeating its result", "key", key.String())
			return stored, nil
		}
		if ctx.Err() != nil {
			// The delivery is over; the engine hands the job out again.
			return nil, ErrHeld
		}
		// The holder is presumed gone: take the step over. Another waiter
		// may be faster, and then this one waits for its result again
		// instead of failing.
		claimed, err = g.journal.Acquire(store, key, g.owner, g.lease)
		if err != nil {
			slog.Warn("journal: the step could not be claimed, running it anyway", "key", key.String(), "err", err)
			return work()
		}
	}
	defer func() {
		if err := g.journal.Release(store, key, g.owner); err != nil {
			slog.Warn("journal: the claim on the step could not be released", "key", key.String(), "err", err)
		}
	}()
	// The claim is ours, but the step may have finished between our lookup
	// and our claim: another delivery may have stored the result and
	// released the claim in between. Without this check the work would run
	// a second time.
	if stored, ok, err := g.journal.Result(store, key); err == nil && ok {
		slog.Info("journal: the step finished elsewhere, repeating its result", "key", key.String())
		return stored, nil
	}
	result, err := work()
	if err != nil {
		return nil, err
	}
	if err := g.journal.Store(store, key, g.owner, result); err != nil {
		slog.Error("journal: the result of the step could not be stored, a redelivery may repeat it", "key", key.String(), "err", err)
	}
	return result, nil
}

// pollInterval is how often a worker which waits for the result of a step
// looks for it.
const pollInterval = 100 * time.Millisecond

// waitForResult waits for the worker which holds the claim to store the
// result of the step. It gives up when the claim expires — the holder is
// presumed gone — or when the context is done.
//
// It returns false when the step has to be taken over, not when it failed.
func (g *Guard) waitForResult(ctx context.Context, key Key) (map[string]any, bool) {
	for {
		if stored, ok, err := g.journal.Result(context.WithoutCancel(ctx), key); err == nil && ok {
			return stored, true
		}
		claim, held, err := g.journal.Claim(context.WithoutCancel(ctx), key)
		if err != nil || !held {
			// The holder released the claim without storing a result: its
			// work failed or it died. The step can be taken over.
			return nil, false
		}
		wait := time.Until(claim.Until)
		if wait <= 0 {
			return nil, false
		}
		if wait > pollInterval {
			wait = pollInterval
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(wait):
		}
	}
}
