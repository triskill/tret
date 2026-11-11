package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type RunResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
	Cancelled bool
}

type Runner struct {
	WorkspaceRoot string
}

const (
	outputLimit  = 32 * 1024
	commandLimit = 16 * 1024
	termGrace    = 200 * time.Millisecond
	drainGrace   = 200 * time.Millisecond
)

func NewRunner(workspace string) (*Runner, error) {
	fi, err := os.Stat(workspace)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("workspace_root must be an existing directory")
	}
	return &Runner{WorkspaceRoot: workspace}, nil
}

func (r *Runner) Run(ctx context.Context, command string, timeout time.Duration) (*RunResult, error) {
	if command == "" || containsNUL(command) {
		return nil, fmt.Errorf("command must be a nonempty string without NUL")
	}
	if !utf8.ValidString(command) {
		return nil, fmt.Errorf("command must be UTF-8")
	}
	if len(command) > commandLimit {
		return nil, fmt.Errorf("command exceeds 16 KiB")
	}
	if timeout <= 0 || timeout > 120*time.Second {
		return nil, fmt.Errorf("timeout must be finite and in (0, 120]")
	}

	start := time.Now()
	if ctx.Err() != nil {
		return &RunResult{Cancelled: true, Duration: 0}, nil
	}

	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = r.WorkspaceRoot
	cmd.Stdin = nil
	cmd.Env = []string{
		"HOME=" + r.WorkspaceRoot,
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TMPDIR=/tmp",
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	pgid := cmd.Process.Pid
	var groupSignal = func(sig syscall.Signal) {
		_ = syscall.Kill(-pgid, sig)
	}

	stdoutBuf := newCappedBuffer(outputLimit)
	stderrBuf := newCappedBuffer(outputLimit)
	truncated := false
	var mu sync.Mutex
	markTrunc := func() { mu.Lock(); truncated = true; mu.Unlock() }

	readDone := make(chan struct{}, 2)
	go func() {
		readPipe(stdoutPipe, stdoutBuf, markTrunc)
		readDone <- struct{}{}
	}()
	go func() {
		readPipe(stderrPipe, stderrBuf, markTrunc)
		readDone <- struct{}{}
	}()

	timedOut := false
	cancelled := false
	killed := false
	stopAt := time.Time{}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

loop:
	for {
		select {
		case err := <-waitCh:
			// process exited; drain pipes briefly
			drainUntil := time.Now().Add(drainGrace)
			for time.Now().Before(drainUntil) {
				select {
				case <-readDone:
				case <-time.After(10 * time.Millisecond):
				}
				// count done
				select {
				case <-readDone:
				default:
				}
				break
			}
			_ = err
			break loop
		case <-ticker.C:
		}
		now := time.Now()
		exited := cmd.ProcessState != nil
		cancelledNow := ctx.Err() != nil
		if cancelledNow {
			cancelled = true
		}
		timedOutNow := !cancelled && now.Sub(start) >= timeout
		if timedOutNow {
			timedOut = true
		}
		if stopAt.IsZero() {
			if cancelled || timedOut || exited {
				if cancelled || timedOut || exited {
					groupSignal(syscall.SIGTERM)
					stopAt = now
				}
			}
		} else {
			if now.Sub(stopAt) >= termGrace && !killed {
				groupSignal(syscall.SIGKILL)
				killed = true
			}
			if killed && exited {
				break loop
			}
			if now.Sub(stopAt) >= termGrace+drainGrace {
				break loop
			}
		}
	}

	// ensure kill
	groupSignal(syscall.SIGKILL)
	stdoutPipe.Close()
	stderrPipe.Close()

	// wait for readers
	for i := 0; i < 2; i++ {
		select {
		case <-readDone:
		case <-time.After(drainGrace):
		}
	}

	duration := time.Since(start)
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	} else {
		// try wait
		_ = cmd.Wait()
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
	}

	mu.Lock()
	tr := truncated
	mu.Unlock()

	return &RunResult{
		Stdout:    stdoutBuf.String(),
		Stderr:    stderrBuf.String(),
		ExitCode:  exitCode,
		Duration:  duration,
		TimedOut:  timedOut,
		Truncated: tr,
		Cancelled: cancelled,
	}, nil
}

type cappedBuffer struct {
	buf   []byte
	limit int
}

func newCappedBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit} }

func (c *cappedBuffer) Write(p []byte) (int, error) {
	avail := c.limit - len(c.buf)
	if avail > 0 {
		n := len(p)
		if n > avail {
			n = avail
		}
		c.buf = append(c.buf, p[:n]...)
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return string(c.buf) }

func readPipe(r io.Reader, buf *cappedBuffer, markTrunc func()) {
	br := bufio.NewReader(r)
	chunk := make([]byte, 65536)
	for {
		n, err := br.Read(chunk)
		if n > 0 {
			before := len(buf.buf)
			_, _ = buf.Write(chunk[:n])
			if len(buf.buf)-before < n {
				markTrunc()
			}
		}
		if err != nil {
			return
		}
	}
}

func containsNUL(s string) bool {
	for _, r := range s {
		if r == 0 {
			return true
		}
	}
	return false
}
