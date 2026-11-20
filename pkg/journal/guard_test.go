package journal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The guard is what makes a redelivery of a job a repeat of its result
// instead of a second execution of its work.
func TestGuardRepeatsTheResult(t *testing.T) {
	j := NewMemory()
	guard := NewGuard(j, "owner-a", time.Minute)
	ctx := context.Background()
	key := Key{JobKey: 1}
	calls := 0
	work := func() (map[string]any, error) {
		calls++
		return map[string]any{"out": "value"}, nil
	}
	first, err := guard.Do(ctx, key, work)
	if err != nil {
		t.Fatal(err)
	}
	second, err := guard.Do(ctx, key, work)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("the work ran %d times", calls)
	}
	if first["out"] != "value" || second["out"] != "value" {
		t.Fatalf("first=%v second=%v", first, second)
	}
}

// A step which another worker is running is waited for, not failed: the
// work is bounded, and failing would spend a retry of the job for a step
// which is about to finish.
func TestGuardWaitsForTheResultOfAHeldStep(t *testing.T) {
	j := NewMemory()
	ctx := context.Background()
	key := Key{JobKey: 2}
	if claimed, err := j.Acquire(ctx, key, "owner-b", time.Minute); err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	// The holder finishes its work a moment later.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = j.Store(ctx, key, "owner-b", map[string]any{"out": "from-the-holder"})
		_ = j.Release(ctx, key, "owner-b")
	}()
	guard := NewGuard(j, "owner-a", time.Minute)
	calls := 0
	result, err := guard.Do(ctx, key, func() (map[string]any, error) {
		calls++
		return map[string]any{"out": "mine"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("the work ran %d times", calls)
	}
	if result["out"] != "from-the-holder" {
		t.Fatalf("result=%v", result)
	}
}

// A step whose holder never finishes — a worker which died with the claim —
// is taken over once the claim expires.
func TestGuardTakesOverAnExpiredStep(t *testing.T) {
	j := NewMemory()
	ctx := context.Background()
	key := Key{JobKey: 3}
	if claimed, err := j.Acquire(ctx, key, "owner-b", 50*time.Millisecond); err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	guard := NewGuard(j, "owner-a", time.Minute)
	calls := 0
	result, err := guard.Do(ctx, key, func() (map[string]any, error) {
		calls++
		return map[string]any{"out": "mine"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("the work ran %d times", calls)
	}
	if result["out"] != "mine" {
		t.Fatalf("result=%v", result)
	}
}

// The step may finish between the lookup of the result and the claim of the
// step. The result is looked up again once the claim is held, or the work
// runs a second time.
func TestGuardRechecksTheResultAfterClaiming(t *testing.T) {
	j := &storeOnAcquire{Journal: NewMemory(), result: map[string]any{"out": "from-the-other"}}
	guard := NewGuard(j, "owner-a", time.Minute)
	calls := 0
	result, err := guard.Do(context.Background(), Key{JobKey: 4}, func() (map[string]any, error) {
		calls++
		return map[string]any{"out": "mine"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("the work ran %d times", calls)
	}
	if result["out"] != "from-the-other" {
		t.Fatalf("result=%v", result)
	}
}

// storeOnAcquire stores the result of the step as soon as it is claimed:
// another delivery finishes the step between the lookup of the result and
// the claim of this delivery.
type storeOnAcquire struct {
	Journal
	result map[string]any
}

func (s *storeOnAcquire) Acquire(ctx context.Context, key Key, owner string, lease time.Duration) (bool, error) {
	claimed, err := s.Journal.Acquire(ctx, key, owner, lease)
	if claimed {
		_ = s.Journal.Store(ctx, key, "the-other-delivery", s.result)
		_ = s.Journal.Release(ctx, key, owner)
	}
	return claimed, err
}

// A waiter which loses the race for the claim waits again instead of
// failing: a failure spends a retry of the job, and with the default retry
// budget that ends the instance with an incident.
func TestGuardWaitsAgainAfterLosingTheTakeover(t *testing.T) {
	j := &loseTheRace{Journal: NewMemory(), result: map[string]any{"out": "from-the-other"}}
	guard := NewGuard(j, "owner-a", time.Minute)
	calls := 0
	result, err := guard.Do(context.Background(), Key{JobKey: 9}, func() (map[string]any, error) {
		calls++
		return map[string]any{"out": "mine"}, nil
	})
	if err != nil {
		t.Fatalf("the waiter failed instead of waiting again: %v", err)
	}
	if calls != 0 {
		t.Fatalf("the work ran %d times", calls)
	}
	if result["out"] != "from-the-other" {
		t.Fatalf("result=%v", result)
	}
}

// loseTheRace loses the race for the claim twice: the step is held by
// another worker, and another waiter takes it over when the holder is gone.
type loseTheRace struct {
	Journal
	mu     sync.Mutex
	calls  int
	result map[string]any
}

func (l *loseTheRace) Acquire(ctx context.Context, key Key, owner string, lease time.Duration) (bool, error) {
	l.mu.Lock()
	l.calls++
	calls := l.calls
	l.mu.Unlock()
	switch calls {
	case 1:
		// Another worker holds the step. Its lease is short, so the waiter
		// gives up on it and tries to take the step over.
		_, _ = l.Journal.Acquire(ctx, key, "the-other-worker", 50*time.Millisecond)
		return false, nil
	case 2:
		// Another waiter was faster and took the step over. Its result
		// follows.
		_, _ = l.Journal.Acquire(ctx, key, "the-other-waiter", time.Minute)
		go func() {
			time.Sleep(20 * time.Millisecond)
			_ = l.Journal.Store(ctx, key, "the-other-waiter", l.result)
		}()
		return false, nil
	default:
		return l.Journal.Acquire(ctx, key, owner, lease)
	}
}

// A step which failed is not remembered: the next delivery runs it again.
func TestGuardDoesNotRememberAFailedStep(t *testing.T) {
	j := NewMemory()
	guard := NewGuard(j, "owner-a", time.Minute)
	ctx := context.Background()
	key := Key{JobKey: 5}
	calls := 0
	failing := func() (map[string]any, error) {
		calls++
		return nil, errors.New("boom")
	}
	if _, err := guard.Do(ctx, key, failing); err == nil {
		t.Fatal("expected the failure of the work")
	}
	if _, err := guard.Do(ctx, key, failing); err == nil {
		t.Fatal("expected the failure of the work")
	}
	if calls != 2 {
		t.Fatalf("the work ran %d times", calls)
	}
}

// A worker without a journal runs its work once per delivery, the way it did
// before the journal existed.
func TestGuardWithoutJournal(t *testing.T) {
	var guard *Guard
	calls := 0
	work := func() (map[string]any, error) {
		calls++
		return map[string]any{"out": "value"}, nil
	}
	for i := 0; i < 2; i++ {
		if _, err := guard.Do(context.Background(), Key{JobKey: 6}, work); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("the work ran %d times", calls)
	}
}

// The result of a step is stored even when the delivery which made it is
// over: the engine hands the job out again, and the next delivery finds it.
func TestGuardStoresTheResultOfAnEndedDelivery(t *testing.T) {
	j := NewMemory()
	guard := NewGuard(j, "owner-a", time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	key := Key{JobKey: 7}
	_, err := guard.Do(ctx, key, func() (map[string]any, error) {
		cancel()
		return map[string]any{"out": "value"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, ok, err := j.Result(context.Background(), key)
	if err != nil || !ok || stored["out"] != "value" {
		t.Fatalf("stored=%v ok=%v err=%v", stored, ok, err)
	}
}

// Two workers of one process must not run the same step at the same time:
// the claim of the first delivery is live when the second delivery arrives.
func TestGuardRefusesALiveClaimOfTheSameOwner(t *testing.T) {
	j := NewMemory()
	ctx := context.Background()
	key := Key{JobKey: 8}
	release := make(chan struct{})
	started := make(chan struct{})
	guard := NewGuard(j, "owner-a", time.Minute)
	var once sync.Once
	work := func() (map[string]any, error) {
		once.Do(func() { close(started) })
		<-release
		return map[string]any{"out": "value"}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := guard.Do(ctx, key, work)
		done <- err
	}()
	<-started
	// The second delivery of the same job finds the claim of the first one.
	claimed, err := j.Acquire(ctx, key, "owner-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("the live claim of the same owner was taken over")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
