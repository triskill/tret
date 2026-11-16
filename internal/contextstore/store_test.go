package contextstore

import (
	"os"
	"strings"
	"sync"
	"testing"
)

func TestLoadEntriesRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Append("run-1", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("run-1", "assistant", "hi there"); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("run-1", "user", "$ ls\nexit: 0"); err != nil {
		t.Fatal(err)
	}
	entries, err := s.LoadEntries("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0] != (Entry{Role: "user", Content: "hello"}) {
		t.Fatalf("entry 0: %+v", entries[0])
	}
	if entries[2].Role != "user" {
		t.Fatalf("entry 2: %+v", entries[2])
	}
}

func TestLoadEntriesMissingIsEmpty(t *testing.T) {
	s := New(t.TempDir())
	entries, err := s.LoadEntries("no-such-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries", len(entries))
	}
}

func TestLoadEntriesRejectsCorruptLines(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Append("run-1", "user", "good"); err != nil {
		t.Fatal(err)
	}
	// hand-append a corrupt line
	f, err := os.OpenFile(s.path("run-1"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	entries, err := s.LoadEntries("run-1")
	if err == nil {
		t.Fatalf("corrupt history accepted: %+v", entries)
	}
}

func TestKeyIsolationAndLimits(t *testing.T) {
	s := New(t.TempDir())
	for _, key := range []string{"a/b", "a_b"} {
		if err := s.Append(key, "user", key); err != nil {
			t.Fatal(err)
		}
		entries, err := s.LoadEntries(key)
		if err != nil || len(entries) != 1 || entries[0].Content != key {
			t.Fatalf("key=%s entries=%v err=%v", key, entries, err)
		}
	}
	if err := s.Append("large", "user", strings.Repeat("x", MaxBytes)); err == nil {
		t.Fatal("oversized entry accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < MaxEntries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Append("many", "user", "x"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := s.Append("many", "user", "overflow"); err == nil {
		t.Fatal("entry cap not enforced")
	}
	if entries, err := s.LoadEntries("many"); err != nil || len(entries) != MaxEntries {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	info, err := os.Stat(s.path("many"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("file permissions=%v", info.Mode())
	}
}
