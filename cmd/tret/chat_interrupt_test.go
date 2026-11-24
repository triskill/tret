package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/triskill/tret/internal/zen"
	"github.com/triskill/tret/pkg/journal"
)

func TestChatInterruptLive(t *testing.T) {
	if os.Getenv("TRET_INTEGRATION") != "1" {
		t.Skip("set TRET_INTEGRATION=1 for a dedicated engine")
	}
	// Run the real CLI signal handler in a subprocess so SIGINT doesn't
	// affect the other tests.
	if os.Getenv("TRET_CHAT_INTERRUPT_CHILD") == "1" {
		runChat(nil)
		return
	}

	rest, grpc := os.Getenv("TRET_INTEGRATION_REST"), os.Getenv("TRET_INTEGRATION_GRPC")
	if rest == "" {
		rest = "http://localhost:8080"
	}
	if grpc == "" {
		grpc = "localhost:9090"
	}
	client := zen.NewClient(rest)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const command = `touch command-started; for ((i=0; i<600; i++)); do if [[ -f command-release ]]; then printf finished; exit 0; fi; sleep 0.05; done; exit 2`
		content, _ := json.Marshal(map[string]string{"action": "shell", "command": command, "answer": ""})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}, "finish_reason": "stop"}}})
	}))
	defer provider.Close()

	dir := t.TempDir()
	cfg := fmt.Sprintf("engine_rest: %q\nengine_grpc: %q\nmodel_profiles:\n  default:\n    base_url: %q\n    model: mock\n", rest, grpc, provider.URL)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(filepath.Join(dir, "chat-output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestChatInterruptLive$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TRET_CHAT_INTERRUPT_CHILD=1")
	cmd.Stdin = strings.NewReader("run a check\ny\n")
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(dir, "command-release")
	defer func() {
		// Don't leave the detached test command waiting if an assertion fails.
		_ = os.WriteFile(release, nil, 0600)
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	readOutput := func() string {
		b, err := os.ReadFile(output.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	await := func(what string, condition func() bool) {
		t.Helper()
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for !condition() {
			select {
			case <-ctx.Done():
				t.Fatalf("waiting for %s: %v\n%s", what, ctx.Err(), readOutput())
			case <-tick.C:
			}
		}
	}

	await("approved command to start", func() bool {
		_, err := os.Stat(filepath.Join(dir, "command-started"))
		return err == nil
	})
	_, session, _ := strings.Cut(readOutput(), "/exit to quit. Session ")
	instanceKey, err := strconv.ParseInt(strings.SplitN(session, "\n", 2)[0], 10, 64)
	if err != nil {
		t.Fatalf("could not find session key: %v\n%s", err, readOutput())
	}
	jobs, err := client.ListJobs(instanceKey)
	if err != nil {
		t.Fatal(err)
	}
	var execKey int64
	for _, job := range jobs {
		if strings.HasPrefix(job.Type, "exec-command-v1-") {
			execKey = job.Key
		}
	}
	if execKey == 0 {
		t.Fatal("command job not found")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	await("instance termination", func() bool {
		instance, err := client.GetInstance(instanceKey)
		if err != nil {
			t.Fatal(err)
		}
		return instance.State == "terminated"
	})
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(readOutput(), "context canceled") {
		t.Fatalf("expected handled interrupt, got %v\n%s", err, readOutput())
	}

	store, err := journal.NewFile(filepath.Join(dir, ".tret", "journal"))
	if err != nil {
		t.Fatal(err)
	}
	result, ok, err := store.Result(ctx, journal.Key{JobKey: execKey})
	execution, _ := result["exec_result"].(map[string]any)
	if err != nil || !ok || execution["stdout"] != "finished" || execution["exit_code"] != float64(0) {
		t.Fatalf("command outcome not journaled: %v %v %v", result, ok, err)
	}
}
