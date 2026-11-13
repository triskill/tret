package contextstore

import (
	"os"
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

func TestLoadEntriesSkipsCorruptLines(t *testing.T) {
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
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Content != "good" {
		t.Fatalf("got %+v", entries)
	}
}
