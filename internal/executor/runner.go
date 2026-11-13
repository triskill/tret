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

	// Own the pipes so Wait cannot close them before readers drain the output.
	stdoutPipe, stdoutWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer stdoutPipe.Close()
	stderrPipe, stderrWriter, err := os.Pipe()
	if err != nil {
		stdoutWriter.Close()
		return nil, err
	}
	defer stderrPipe.Close()
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	err = cmd.Start()
	stdoutWriter.Close()
	stderrWriter.Close()
	if err != nil {
		return nil, err
	}

	pgid := cmd.Process.Pid
	groupSignal := func(sig syscall.Signal) {
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

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	timedOut := false
	cancelled := false
	var waitErr error
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		cancelled = true
	case <-timer.C:
		cancelled = ctx.Err() != nil
		timedOut = !cancelled
	}
	if cancelled || timedOut {
		groupSignal(syscall.SIGTERM)
		grace := time.NewTimer(termGrace)
		select {
		case waitErr = <-waitCh:
		case <-grace.C:
			groupSignal(syscall.SIGKILL)
			waitErr = <-waitCh
		}
		grace.Stop()
	}
	// Also clean up descendants when the shell exits without waiting for them.
	groupSignal(syscall.SIGKILL)

	// Count both readers, then join them even if inherited pipes remain open.
	// Only after the join is it safe to read their buffers.
	drainTimer := time.NewTimer(drainGrace)
	defer drainTimer.Stop()
	for remaining := 2; remaining > 0; {
		select {
		case <-readDone:
			remaining--
		case <-drainTimer.C:
			stdoutPipe.Close()
			stderrPipe.Close()
			for ; remaining > 0; remaining-- {
				<-readDone
			}
		}
	}

	duration := time.Since(start)
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	} else if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
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
