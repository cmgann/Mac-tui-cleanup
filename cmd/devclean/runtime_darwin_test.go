package main

import (
	"os"
	"runtime"
	"syscall"
	"testing"
)

func TestNativeMacRuntime(t *testing.T) {
	expected := os.Getenv("DEVCLEAN_EXPECT_ARCH")
	if expected == "" {
		t.Skip("CI sets the expected native architecture")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != expected {
		t.Fatalf("expected native darwin/%s, executing %s/%s", expected, runtime.GOOS, runtime.GOARCH)
	}
	if expected == "arm64" {
		value, e := syscall.SysctlUint32("hw.optional.arm64")
		if e != nil || value != 1 {
			t.Fatalf("Apple Silicon hardware verification failed: %d %v", value, e)
		}
	}
	t.Logf("Native runtime verified: %s on %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
