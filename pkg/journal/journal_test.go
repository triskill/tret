package journal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryStoresAndReturnsTheResult(t *testing.T) {
	j := NewMemory()
	ctx := context.Background()
	key := Key{JobKey: 42}
	if _, ok, err := j.Result(ctx, key); err != nil || ok {
		t.Fatalf("expected no result, got ok=%v err=%v", ok, err)
	}
	if err := j.Store(ctx, key, "owner-a", map[string]any{"out": "value"}); err != nil {
		t.Fatal(err)
	}
	result, ok, err := j.Result(ctx, key)
	if err != nil || !ok || result["out"] != "value" {
		t.Fatalf("result=%v ok=%v err=%v", result, ok, err)
	}
}

func TestMemoryClaimIsExclusive(t *testing.T) {
	j := NewMemory()
	ctx := context.Background()
	key := Key{JobKey: 7}
	claimed, err := j.Acquire(ctx, key, "owner-a", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	claimed, err = j.Acquire(ctx, key, "owner-b", time.Minute)
	if err != nil || claimed {
		t.Fatalf("a second owner took the step: claimed=%v err=%v", claimed, err)
	}
	if err := j.Release(ctx, key, "owner-a"); err != nil {
		t.Fatal(err)
	}
	claimed, err = j.Acquire(ctx, key, "owner-b", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("the step was not released: claimed=%v err=%v", claimed, err)
	}
}

func TestMemoryClaimExpires(t *testing.T) {
	j := NewMemory()
	ctx := context.Background()
	key := Key{JobKey: 8}
	if claimed, _ := j.Acquire(ctx, key, "owner-a", time.Millisecond); !claimed {
		t.Fatal("not claimed")
	}
	time.Sleep(5 * time.Millisecond)
	claimed, err := j.Acquire(ctx, key, "owner-b", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("the claim of a dead worker was not taken over: claimed=%v err=%v", claimed, err)
	}
}

// The file journal keeps the results of the steps across the restart of a
// worker, and its claims are decided by the filesystem, so two workers on one
// node do not run the same step at the same time.
func TestFileJournalAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	first, err := NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := Key{JobKey: 99}
	if err := first.Store(ctx, key, "owner-a", map[string]any{"out": "value"}); err != nil {
		t.Fatal(err)
	}
	// A second handle on the same directory sees the stored result.
	second, err := NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	result, ok, err := second.Result(ctx, key)
	if err != nil || !ok || result["out"] != "value" {
		t.Fatalf("result=%v ok=%v err=%v", result, ok, err)
	}
}

func TestFileJournalClaimIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := Key{JobKey: 100}
	claimed, err := first.Acquire(ctx, key, "owner-a", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	claimed, err = second.Acquire(ctx, key, "owner-b", time.Minute)
	if err != nil || claimed {
		t.Fatalf("a second owner took the step: claimed=%v err=%v", claimed, err)
	}
	// The owner which holds the claim releases it.
	if err := first.Release(ctx, key, "owner-a"); err != nil {
		t.Fatal(err)
	}
	claimed, err = second.Acquire(ctx, key, "owner-b", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("the step was not released: claimed=%v err=%v", claimed, err)
	}
}

func TestFileJournalTakesOverAnExpiredClaim(t *testing.T) {
	dir := t.TempDir()
	first, err := NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := Key{JobKey: 101}
	if claimed, _ := first.Acquire(ctx, key, "owner-a", time.Millisecond); !claimed {
		t.Fatal("not claimed")
	}
	// The worker which held the claim died: the file is older than the
	// lease, so another worker takes the step over.
	time.Sleep(10 * time.Millisecond)
	second, err := NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := second.Acquire(ctx, key, "owner-b", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("the claim of a dead worker was not taken over: claimed=%v err=%v", claimed, err)
	}
}

// Two workers which take the same expired claim over must not both win: the
// takeover is serialized, or the second one removes the claim the first one
// has just taken and both run the work.
func TestFileJournalTakeoverIsAtomic(t *testing.T) {
	for round := 0; round < 50; round++ {
		dir := t.TempDir()
		first, err := NewFile(dir)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		key := Key{JobKey: int64(200 + round)}
		// A claim whose holder died.
		if claimed, err := first.Acquire(ctx, key, "owner-dead", time.Millisecond); err != nil || !claimed {
			t.Fatalf("claimed=%v err=%v", claimed, err)
		}
		time.Sleep(5 * time.Millisecond)
		var winners atomic.Int32
		var group sync.WaitGroup
		for i := 0; i < 2; i++ {
			group.Add(1)
			go func(n int) {
				defer group.Done()
				contender, err := NewFile(dir)
				if err != nil {
					t.Error(err)
					return
				}
				claimed, err := contender.Acquire(ctx, key, fmt.Sprintf("owner-%d", n), time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if claimed {
					winners.Add(1)
				}
			}(i)
		}
		group.Wait()
		if winners.Load() != 1 {
			t.Fatalf("round %d: %d workers took the claim over", round, winners.Load())
		}
	}
}

// While one worker holds the gate, no other worker may change the claim of
// the step: a worker which takes an expired claim over would otherwise
// remove the claim another worker has just created.
func TestFileJournalGateSerializesEveryClaimChange(t *testing.T) {
	dir := t.TempDir()
	j, err := NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := Key{JobKey: 999}
	// The gate is held by a worker which is in the middle of a takeover.
	release, err := j.gate(key)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(chan bool, 1)
	go func() {
		taken, err := j.Acquire(ctx, key, "owner-b", time.Minute)
		if err != nil {
			claimed <- false
			return
		}
		claimed <- taken
	}()
	select {
	case taken := <-claimed:
		release()
		t.Fatalf("a claim was taken while the gate was held: %v", taken)
	case <-time.After(200 * time.Millisecond):
		// The other worker waits for the gate.
	}
	release()
	select {
	case taken := <-claimed:
		if !taken {
			t.Fatal("the claim was not taken after the gate was released")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not take the claim after the gate was released")
	}
}

// A worker which takes an expired claim over must not remove the claim a
// worker created in the meantime: both would run the work. Every change of
// the claim is serialized, so the claim of one worker is never the claim of
// two.
func TestFileJournalClaimsStayExclusiveUnderChurn(t *testing.T) {
	for round := 0; round < 100; round++ {
		dir := t.TempDir()
		j, err := NewFile(dir)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		key := Key{JobKey: int64(400 + round)}
		// A claim which expired.
		if claimed, err := j.Acquire(ctx, key, "owner-dead", time.Millisecond); err != nil || !claimed {
			t.Fatalf("claimed=%v err=%v", claimed, err)
		}
		time.Sleep(3 * time.Millisecond)
		var holders atomic.Int32
		var violations atomic.Int32
		var group sync.WaitGroup
		// A worker which takes the claim over and releases it again and
		// again, and a worker which takes the expired claim over.
		for _, owner := range []string{"churner", "taker"} {
			group.Add(1)
			go func(owner string) {
				defer group.Done()
				contender, err := NewFile(dir)
				if err != nil {
					t.Error(err)
					return
				}
				for i := 0; i < 30; i++ {
					claimed, err := contender.Acquire(ctx, key, owner, time.Minute)
					if err != nil {
						return
					}
					if !claimed {
						continue
					}
					if holders.Add(1) > 1 {
						violations.Add(1)
					}
					time.Sleep(time.Millisecond)
					holders.Add(-1)
					_ = contender.Release(ctx, key, owner)
				}
			}(owner)
		}
		group.Wait()
		if violations.Load() != 0 {
			t.Fatalf("round %d: two workers held the claim at the same time", round)
		}
	}
}

// The result is written atomically: a reader never sees half of it.
func TestFileJournalResultIsAtomic(t *testing.T) {
	dir := t.TempDir()
	j, err := NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := Key{JobKey: 102}
	if err := j.Store(ctx, key, "owner-a", map[string]any{"out": "value"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			t.Fatalf("the journal kept %q next to the results", entry.Name())
		}
	}
}
