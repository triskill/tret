package contextstore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// These are storage bounds, not a model token-window estimate.
const MaxBytes = 1 << 20
const MaxEntries = 100

type Entry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Store struct {
	Dir string
	mu  sync.Mutex
}

func New(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) path(key string) string {
	return filepath.Join(s.Dir, "contexts", fmt.Sprintf("%x.jsonl", sha256.Sum256([]byte(key))))
}

// Caller holds mu. Missing history is empty; corrupt or oversized history fails
// closed rather than silently changing the conversation seen by the model.
func (s *Store) read(key string) ([]Entry, int, error) {
	f, err := os.Open(s.path(key))
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(b) > MaxBytes {
		return nil, 0, fmt.Errorf("conversation exceeds %d bytes", MaxBytes)
	}
	var entries []Entry
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, 0, fmt.Errorf("corrupt conversation line %d: %w", i+1, err)
		}
		if entry.Role == "" {
			return nil, 0, fmt.Errorf("corrupt conversation line %d: role required", i+1)
		}
		entries = append(entries, entry)
		if len(entries) > MaxEntries {
			return nil, 0, fmt.Errorf("conversation exceeds %d entries", MaxEntries)
		}
	}
	return entries, len(b), nil
}

func (s *Store) LoadEntries(key string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _, err := s.read(key)
	return entries, err
}

func (s *Store) Load(key string) (string, error) {
	entries, err := s.LoadEntries(key)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("%s: %s", entry.Role, entry.Content))
	}
	return strings.Join(lines, "\n"), nil
}

// Append serializes writes within this Store. Sharing a state directory across
// processes is unsupported. Existing paths retain their current permissions.
func (s *Store) Append(key, role, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if role == "" {
		return fmt.Errorf("conversation role is required")
	}
	entries, size, err := s.read(key)
	if err != nil {
		return err
	}
	if len(entries) >= MaxEntries {
		return fmt.Errorf("conversation exceeds %d entries", MaxEntries)
	}
	b, err := json.Marshal(Entry{Role: role, Content: content})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if size+len(b) > MaxBytes {
		return fmt.Errorf("conversation exceeds %d bytes", MaxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(s.path(key)), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(key), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
