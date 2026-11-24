package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
)

// fakeRenewer records the lock extensions the registry asks for.
type fakeRenewer struct {
	mu    sync.Mutex
	calls []renewal
	// err is the answer of the engine, if any.
	err error
}

type renewal struct {
	key      int64
	duration time.Duration
	at       time.Time
}

func (f *fakeRenewer) ExtendDeliveryLock(_ context.Context, job *proto.WaitingJob, d time.Duration) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, renewal{key: job.GetKey(), duration: d, at: time.Now()})
	if f.err != nil {
		return time.Time{}, f.err
	}
	return time.Now().Add(d), nil
}

// blockingRenewer never answers: the caller gives up on the call.
type blockingRenewer struct {
	mu    sync.Mutex
	calls int
}

func (b *blockingRenewer) ExtendDeliveryLock(ctx context.Context, _ *proto.WaitingJob, _ time.Duration) (time.Time, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	<-ctx.Done()
	return time.Time{}, ctx.Err()
}

func (b *blockingRenewer) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func (f *fakeRenewer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRenewer) first() (renewal, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return renewal{}, false
	}
	return f.calls[0], true
}

// waitFor polls until the condition holds or the deadline passes.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

// A lock which lapses while the job runs hands the job to a second worker:
// the side effect runs twice. The registry therefore renews the lock while
// the handler runs.
func TestRegistryRenewsTheLockOfARunningJob(t *testing.T) {
	r := NewRegistry()
	renewer := &fakeRenewer{}
	r.SetRenewer(renewer)
	release := make(chan struct{})
	started := make(chan struct{})
	handler := func(context.Context, *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
		close(started)
		<-release
		return nil, nil
	}
	const lock = 80 * time.Millisecond
	if err := r.Register(Spec{JobType: "slow-v1", LockDuration: lock}, handler); err != nil {
		t.Fatal(err)
	}
	typ, key := "slow-v1", int64(7)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Handle(context.Background(), &proto.WaitingJob{Type: &typ, Key: &key})
	}()
	<-started
	waitFor(t, "the lock of the running job was not renewed", func() bool { return renewer.count() > 0 })
	call, _ := renewer.first()
	if call.key != key {
		t.Errorf("renewed the lock of job %d, want %d", call.key, key)
	}
	if call.duration != lock {
		t.Errorf("renewed the lock for %s, want the lock duration of the subscription (%s)", call.duration, lock)
	}
	close(release)
	<-done
	// The lock of a job which is done is of no use any more.
	calls := renewer.count()
	time.Sleep(250 * time.Millisecond)
	if renewer.count() != calls {
		t.Errorf("the lock was renewed %d times after the job ended", renewer.count()-calls)
	}
}

// The engine tells the worker when the lock of the delivery lapses. The
// renewal must come before that deadline, even when the lock of the
// subscription is far longer than the remaining time.
func TestRegistryRenewsBeforeTheDeadlineOfTheEngine(t *testing.T) {
	r := NewRegistry()
	renewer := &fakeRenewer{}
	r.SetRenewer(renewer)
	release := make(chan struct{})
	started := make(chan struct{})
	handler := func(context.Context, *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
		close(started)
		<-release
		return nil, nil
	}
	if err := r.Register(Spec{JobType: "slow-v1", LockDuration: 10 * time.Second}, handler); err != nil {
		t.Fatal(err)
	}
	defer close(release)
	typ, key := "slow-v1", int64(9)
	deadline := time.Now().Add(500 * time.Millisecond)
	lockUntil := deadline.UnixMilli()
	go r.Handle(context.Background(), &proto.WaitingJob{Type: &typ, Key: &key, LockUntil: &lockUntil})
	<-started
	waitFor(t, "the lock was not renewed before the deadline of the engine", func() bool { return renewer.count() > 0 })
	call, _ := renewer.first()
	if !call.at.Before(deadline) {
		t.Errorf("the lock was renewed at %s, after the deadline of the engine (%s)", call.at, deadline)
	}
}

// The side effect of a job outlives the stream it was delivered on: the
// engine cancels the job context when the stream breaks. The lock must be
// renewed then too, or the engine hands the job to another worker while the
// side effect is still running.
func TestRegistryRenewsAfterTheJobStreamBroke(t *testing.T) {
	r := NewRegistry()
	renewer := &fakeRenewer{}
	r.SetRenewer(renewer)
	release := make(chan struct{})
	started := make(chan struct{})
	handler := func(context.Context, *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
		close(started)
		<-release
		return nil, nil
	}
	if err := r.Register(Spec{JobType: "slow-v1", LockDuration: 80 * time.Millisecond}, handler); err != nil {
		t.Fatal(err)
	}
	typ, key := "slow-v1", int64(11)
	ctx, cancel := context.WithCancel(context.Background())
	go r.Handle(ctx, &proto.WaitingJob{Type: &typ, Key: &key})
	<-started
	// The stream breaks: the engine cancels the context of the delivery.
	cancel()
	waitFor(t, "the lock was not renewed after the stream broke", func() bool { return renewer.count() > 0 })
	close(release)
}

// A registry without a renewer runs the job as before: the renewal is an
// addition, not a requirement.
func TestRegistryRunsJobsWithoutARenewer(t *testing.T) {
	r := NewRegistry()
	called := false
	if err := r.Register(Spec{JobType: "example-v1"}, func(context.Context, *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
		called = true
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	typ := "example-v1"
	if _, err := r.Handle(context.Background(), &proto.WaitingJob{Type: &typ}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("the job did not run")
	}
}

// The lock of a job which the engine no longer holds is gone: the job is in
// the hands of another worker, and renewing it again would be pointless.
func TestRenewalEndsWhenTheLockIsGone(t *testing.T) {
	renewer := &fakeRenewer{err: zenclient.ErrLockNotHeld}
	typ, key := "slow-v1", int64(21)
	past := time.Now().Add(-time.Minute).UnixMilli()
	job := &proto.WaitingJob{Type: &typ, Key: &key, LockUntil: &past}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go renewUntil(ctx, renewer, job, 400*time.Millisecond, time.Second)
	waitFor(t, "the renewal did not happen", func() bool { return renewer.count() > 0 })
	time.Sleep(300 * time.Millisecond)
	if renewer.count() != 1 {
		t.Errorf("the renewal was attempted %d times after the engine refused it", renewer.count())
	}
}

// A renewal which the worker gives up on leaves the zenclient with a waiter
// which swallows the answers of the engine. The renewal of that job ends,
// rather than asking again and again.
func TestRenewalEndsAfterAnAbandonedCall(t *testing.T) {
	renewer := &blockingRenewer{}
	typ, key := "slow-v1", int64(23)
	past := time.Now().Add(-time.Minute).UnixMilli()
	job := &proto.WaitingJob{Type: &typ, Key: &key, LockUntil: &past}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go renewUntil(ctx, renewer, job, 400*time.Millisecond, 50*time.Millisecond)
	waitFor(t, "the renewal did not happen", func() bool { return renewer.count() > 0 })
	time.Sleep(300 * time.Millisecond)
	if renewer.count() != 1 {
		t.Errorf("the renewal was attempted %d times after a call was given up on", renewer.count())
	}
}

// An engine which is busy answers with an error and keeps the lock: the
// renewal is tried again.
func TestRenewalRetriesAfterATransientError(t *testing.T) {
	renewer := &fakeRenewer{err: errors.New("the leader of the partition is unavailable")}
	typ, key := "slow-v1", int64(25)
	past := time.Now().Add(-time.Minute).UnixMilli()
	job := &proto.WaitingJob{Type: &typ, Key: &key, LockUntil: &past}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go renewUntil(ctx, renewer, job, 400*time.Millisecond, time.Second)
	waitFor(t, "the renewal did not happen", func() bool { return renewer.count() > 0 })
	waitFor(t, "the renewal was not tried again", func() bool { return renewer.count() > 1 })
}
