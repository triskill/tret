package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	d := t.TempDir()
	r, err := NewRunner(d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunnerQuotingAndPipes(t *testing.T) {
	r := newTestRunner(t)
	res, err := r.Run(context.Background(), `printf 'a b' | tr ' ' '_'`, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Stdout) != "a_b" {
		t.Fatalf("stdout=%q", res.Stdout)
	}
}

func TestRunnerExitCode(t *testing.T) {
	r := newTestRunner(t)
	res, err := r.Run(context.Background(), "exit 7", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
}

func TestRunnerTimeout(t *testing.T) {
	r := newTestRunner(t)
	res, err := r.Run(context.Background(), "sleep 5", 1*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatal("expected timeout")
	}
}

func TestRunnerTruncation(t *testing.T) {
	r := newTestRunner(t)
	// generate >32KiB
	cmd := "python3 -c \"print('x'*40000)\""
	res, err := r.Run(context.Background(), cmd, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("expected truncation")
	}
	if len(res.Stdout) > outputLimit {
		t.Fatalf("stdout too long: %d", len(res.Stdout))
	}
}

func TestRunnerEmptyCommand(t *testing.T) {
	r := newTestRunner(t)
	_, err := r.Run(context.Background(), "", 10*time.Second)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunnerWorkspace(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "f.txt"), []byte("hi"), 0644)
	r, _ := NewRunner(d)
	res, _ := r.Run(context.Background(), "cat f.txt", 10*time.Second)
	if strings.TrimSpace(res.Stdout) != "hi" {
		t.Fatalf("stdout=%q", res.Stdout)
	}
}

func TestRunnerDrainsBothStreams(t *testing.T) {
	r := newTestRunner(t)
	for i := 0; i < 20; i++ {
		res, err := r.Run(context.Background(), `printf '%032768d' 0; printf '%032768d' 0 >&2`, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Repeat("0", outputLimit)
		if res.Stdout != want || res.Stderr != want || res.Truncated {
			t.Fatalf("stdout=%d stderr=%d truncated=%v", len(res.Stdout), len(res.Stderr), res.Truncated)
		}
	}
}

func TestRunnerCancellationKillsIgnoringProcess(t *testing.T) {
	r := newTestRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := filepath.Join(r.WorkspaceRoot, "ready")
	result := make(chan *RunResult, 1)
	errors := make(chan error, 1)
	go func() {
		res, err := r.Run(ctx, `trap '' TERM; touch ready; while :; do :; done`, 10*time.Second)
		result <- res
		errors <- err
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("command did not start")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case res := <-result:
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		if !res.Cancelled || res.TimedOut || res.ExitCode != -1 {
			t.Fatalf("unexpected result: %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled command did not stop")
	}
}

func TestRunnerDoesNotWaitForBackgroundDescendants(t *testing.T) {
	r := newTestRunner(t)
	start := time.Now()
	res, err := r.Run(context.Background(), `sleep 30 & printf done`, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "done" || res.ExitCode != 0 || res.TimedOut || res.Cancelled {
		t.Fatalf("unexpected result: %+v", res)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("waited for background descendant")
	}
}
