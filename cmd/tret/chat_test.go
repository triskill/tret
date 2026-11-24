package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatLive(t *testing.T) {
	if os.Getenv("TRET_INTEGRATION") != "1" {
		t.Skip("set TRET_INTEGRATION=1 for a dedicated engine")
	}
	dir := t.TempDir()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		content := `{"action":"answer","command":"","answer":"Tests passed."}`
		if n == 1 {
			content = `{"action":"shell","command":"printf verified > chat-marker; printf verified","answer":"Run a small check"}`
		}
		if n == 3 {
			content = `{"action":"answer","command":"","answer":"Second turn remembered."}`
		}
		if n == 3 && len(request["messages"].([]any)) < 6 {
			t.Error("conversation history missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}}})
	}))
	defer provider.Close()
	configPath := filepath.Join(dir, "config.yaml")
	config := fmt.Sprintf("model_profiles:\n  default:\n    base_url: %s\n    model: mock\njournal_dir: %s\n", provider.URL, filepath.Join(dir, "journal"))
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out strings.Builder
	err := executeChat(ctx, []string{"-config", configPath}, strings.NewReader("run a check\ny\nremember it\n/exit\n"), &out)
	if err != nil {
		t.Fatalf("chat: %v\n%s", err, out.String())
	}
	marker, err := os.ReadFile(filepath.Join(dir, "chat-marker"))
	if err != nil || string(marker) != "verified" {
		t.Fatalf("command did not run in cwd: %q %v\n%s", marker, err, out.String())
	}
	if calls.Load() != 3 || !strings.Contains(out.String(), "Tests passed.") || !strings.Contains(out.String(), "Second turn remembered.") {
		t.Fatalf("calls=%d transcript:\n%s", calls.Load(), out.String())
	}
}
