package journal

import (
	"context"
	"sync"
	"time"
)

// Memory keeps the results of the steps in the process. It survives neither
// a restart nor another process, so it is the journal of a test and of a
// single worker which never loses a job.
type Memory struct {
	mu      sync.Mutex
	results map[string]map[string]any
	claims  map[string]claim
}

type claim struct {
	owner string
	until time.Time
}

// NewMemory returns an empty in-memory journal.
func NewMemory() *Memory {
	return &Memory{results: map[string]map[string]any{}, claims: map[string]claim{}}
}

func (m *Memory) Result(_ context.Context, key Key) (map[string]any, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result, ok := m.results[key.String()]
	return result, ok, nil
}

func (m *Memory) Acquire(_ context.Context, key Key, owner string, lease time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := key.String()
	if held, ok := m.claims[name]; ok && time.Now().Before(held.until) && held.owner != owner {
		return false, nil
	}
	m.claims[name] = claim{owner: owner, until: time.Now().Add(lease)}
	return true, nil
}

func (m *Memory) Release(_ context.Context, key Key, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := key.String()
	if held, ok := m.claims[name]; ok && held.owner == owner {
		delete(m.claims, name)
	}
	return nil
}

func (m *Memory) Store(_ context.Context, key Key, _ string, result map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results[key.String()] = result
	return nil
}
