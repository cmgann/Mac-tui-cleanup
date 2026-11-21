package runner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestArgumentArraysAndCancellation(t *testing.T) {
	r := Exec{}
	text := "literal $(touch /never-created) ; && `whoami`"
	got, e := r.Run(context.Background(), "/bin/echo", text)
	if e != nil || strings.TrimSpace(got) != text {
		t.Fatal("arguments interpreted", got, e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, e = r.Run(ctx, "/bin/sleep", "10"); e == nil {
		t.Fatal("cancellation ignored")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("slow cancellation")
	}
}
func TestBoundedOutput(t *testing.T) {
	var b limited
	data := []byte(strings.Repeat("x", 3<<20))
	n, e := b.Write(data)
	if e != nil || n != len(data) || b.b.Len() != 2<<20 || !b.overflow {
		t.Fatal(n, e, b.b.Len())
	}
}

func TestRunnerOutputHelper(t *testing.T) {
	if os.Getenv("DEVCLEAN_STREAM_TEST_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "stdout-before-completion")
	fmt.Fprintln(os.Stderr, "stderr-before-completion")
	time.Sleep(15 * time.Second)
}
func TestStreamingArrivesBeforeCompletionAndCancels(t *testing.T) {
	t.Setenv("DEVCLEAN_STREAM_TEST_HELPER", "1")
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := make(chan bool, 1)
	stderr := make(chan bool, 1)
	done := make(chan error, 1)
	ctx = WithOutput(ctx, func(event OutputEvent) {
		if event.Kind != "output" {
			return
		}
		if strings.Contains(event.Text, "stdout-before-completion") {
			select {
			case stdout <- true:
			default:
			}
		}
		if strings.Contains(event.Text, "stderr-before-completion") {
			select {
			case stderr <- true:
			default:
			}
		}
	})
	go func() { _, e := (Exec{}).Run(ctx, binary, "-test.run=^TestRunnerOutputHelper$"); done <- e }()
	for _, ch := range []chan bool{stdout, stderr} {
		select {
		case <-ch:
		case e := <-done:
			t.Fatal("command completed before streaming", e)
		case <-time.After(5 * time.Second):
			t.Fatal("live output blocked until completion")
		}
	}
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancellation did not surface")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("streaming command failed to cancel")
	}
}
