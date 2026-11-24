package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestChatEscapesEngineError(t *testing.T) {
	if url := os.Getenv("TRET_TEST_CHAT_ERROR_URL"); url != "" {
		runChat([]string{"-engine-rest", url})
		return
	}
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unexpected\x1b\rengine response", http.StatusBadGateway)
	}))
	defer engine.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestChatEscapesEngineError$")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "TRET_TEST_CHAT_ERROR_URL="+engine.URL)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), `unexpected\x1b\rengine response`) || strings.ContainsAny(string(output), "\x1b\r") {
		t.Fatalf("expected a safely displayed deploy failure, got %v: %q", err, output)
	}
}
