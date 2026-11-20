package journal

import (
	"context"
	"os"
	"path/filepath"
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
