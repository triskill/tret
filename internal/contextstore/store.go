package contextstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Entry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Store struct {
	Dir string
}

func New(dir string) *Store {
	return &Store{Dir: dir}
}

func (s *Store) path(key string) string {
	safe := strings.ReplaceAll(key, "/", "_")
	return filepath.Join(s.Dir, "contexts", safe+".jsonl")
}

func (s *Store) Load(key string) (string, error) {
	b, err := os.ReadFile(s.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", e.Role, e.Content))
	}
	return strings.Join(lines, "\n"), nil
}

func (s *Store) Append(key, role, content string) error {
	if err := os.MkdirAll(filepath.Dir(s.path(key)), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(key), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(Entry{Role: role, Content: content})
	_, err = f.Write(append(b, '\n'))
	return err
}
