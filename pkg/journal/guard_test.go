package journal

import (
	"context"
	"errors"
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

// A step another worker is running is not run a second time: the caller
// fails the job and the engine hands it out again.
func TestGuardReportsAHeldStep(t *testing.T) {
	j := NewMemory()
	ctx := context.Background()
	key := Key{JobKey: 2}
	if claimed, err := j.Acquire(ctx, key, "owner-b", time.Minute); err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	guard := NewGuard(j, "owner-a", time.Minute)
	calls := 0
	_, err := guard.Do(ctx, key, func() (map[string]any, error) {
		calls++
		return nil, nil
	})
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("expected ErrHeld, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("the work ran %d times", calls)
	}
}

// A step which failed is not remembered: the next delivery runs it again.
func TestGuardDoesNotRememberAFailedStep(t *testing.T) {
	j := NewMemory()
	guard := NewGuard(j, "owner-a", time.Minute)
	ctx := context.Background()
	key := Key{JobKey: 3}
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
		if _, err := guard.Do(context.Background(), Key{JobKey: 4}, work); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("the work ran %d times", calls)
	}
}
