package session

import (
	"encoding/json"
	"os"
	"sync"
)

type Message struct {
	Role    string `json:"role"` // "user", "assistant", "system"
	Content string `json:"content"`
}

type History struct {
	Messages []Message `json:"messages"`
}

type Manager struct {
	FilePath string
	mu       sync.Mutex
	data     History
}

func NewManager(filePath string) (*Manager, error) {
	m := &Manager{FilePath: filePath, data: History{Messages: []Message{}}}
	if _, err := os.Stat(filePath); err == nil {
		file, err := os.ReadFile(filePath)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(file, &m.data); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) AddMessage(role, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Messages = append(m.data.Messages, Message{Role: role, Content: content})
}

func (m *Manager) GetHistory() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data.Messages
}

func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.FilePath, data, 0644)
}
