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
