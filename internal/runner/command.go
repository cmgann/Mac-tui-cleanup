package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Runner interface {
	Run(context.Context, string, ...string) (string, error)
}
type Exec struct{}
type limited struct {
	mu       sync.Mutex
	b        bytes.Buffer
	overflow bool
}

func (b *limited) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n > (2<<20)-b.b.Len() {
		b.overflow = true
	}
	if b.b.Len() < 2<<20 {
		left := (2 << 20) - b.b.Len()
		b.b.Write(p[:min(left, n)])
	}
	return n, nil
}
func (Exec) Run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ANALYTICS=1", "LC_ALL=C")
	var out, stderr limited
	emit, _ := ctx.Value(outputKey{}).(func(OutputEvent))
	command := name + " " + strings.Join(args, " ")
	var outputMu sync.Mutex
	cmd.Stdout = streamCapture{destination: &out, emit: emit, stream: "stdout", command: command, mu: &outputMu}
	cmd.Stderr = streamCapture{destination: &stderr, emit: emit, stream: "stderr", command: command, mu: &outputMu}
	if emit != nil {
		emit(OutputEvent{Kind: "command", Command: command})
	}
	err := cmd.Run()
	s := out.b.String()
	if err != nil {
		return s, fmt.Errorf("%s: %w: %s", name, err, stderr.b.String())
	}
	if out.overflow || stderr.overflow {
		return s, fmt.Errorf("%s: command output exceeded 2 MiB; incomplete output cannot authorize cleanup", name)
	}
	return s, nil
}
