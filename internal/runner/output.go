package runner

import (
	"context"
	"io"
	"sync"
)

type OutputEvent struct{ Kind, Command, Stream, Text string }
type outputKey struct{}

// WithOutput observes bounded output while commands execute. The callback must
// return promptly; command arguments remain arrays and never enter a shell.
func WithOutput(ctx context.Context, emit func(OutputEvent)) context.Context {
	return context.WithValue(ctx, outputKey{}, emit)
}

type streamCapture struct {
	destination     io.Writer
	emit            func(OutputEvent)
	stream, command string
	mu              *sync.Mutex
}

func (w streamCapture) Write(p []byte) (int, error) {
	n, e := w.destination.Write(p)
	if w.emit != nil {
		w.mu.Lock()
		for start := 0; start < n; start += 4096 {
			end := min(n, start+4096)
			w.emit(OutputEvent{Kind: "output", Command: w.command, Stream: w.stream, Text: string(p[start:end])})
		}
		w.mu.Unlock()
	}
	return n, e
}

func WithoutOutput(ctx context.Context) context.Context { return WithOutput(ctx, nil) }
