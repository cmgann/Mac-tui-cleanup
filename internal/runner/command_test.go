package runner

import (
	"context"
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
