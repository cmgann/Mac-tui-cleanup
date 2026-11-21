package schedule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls  []string
	fail   string
	loaded bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	s := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, s)
	if f.fail != "" && strings.Contains(s, f.fail) {
		return "", errors.New("session unavailable")
	}
	if strings.Contains(s, "launchctl print") && !f.loaded {
		return "", errors.New("not loaded")
	}
	if strings.Contains(s, "bootstrap") {
		f.loaded = true
	}
	if strings.Contains(s, "bootout") {
		f.loaded = false
	}
	return "", nil
}
func manager(t *testing.T, platform string) (Manager, *fakeRunner) {
	t.Helper()
	home, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	exe := filepath.Join(home, "devclean with spaces")
	os.WriteFile(exe, []byte("fixture executable"), 0700)
	f := &fakeRunner{}
	return Manager{OS: platform, Home: home, ConfigDir: filepath.Join(home, "config"), Executable: exe, Path: "/usr/bin:/bin", UID: 501, Run: f}, f
}
func TestNativeSchedulesAreReadOnlyAndExplicit(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			m, f := manager(t, platform)
			before, e := m.Status(context.Background())
			if e != nil || before.Enabled || len(f.calls) != 0 {
				t.Fatal(before, e)
			}
			if e = m.Install(context.Background(), "weekly", 9); e != nil {
				t.Fatal(e)
			}
			for _, path := range m.paths() {
				b, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if !strings.Contains(string(b), marker) || strings.Contains(string(b), "--clean") {
					t.Fatal(string(b))
				}
			}
			data, _ := os.ReadFile(m.paths()[0])
			if !strings.Contains(string(data), "--scheduled-scan") {
				t.Fatal("job does not enforce read-only mode")
			}
			if e = m.Disable(context.Background()); e != nil {
				t.Fatal(e)
			}
			for _, path := range m.paths() {
				if _, e = os.Stat(path); !os.IsNotExist(e) {
					t.Fatal("off left schedule file", e)
				}
			}
			for _, call := range f.calls {
				if strings.Contains(call, "sudo") || strings.Contains(call, "sh -c") {
					t.Fatal("unsafe scheduler command", call)
				}
			}
		})
	}
}
func TestSchedulerFailureRollsBackAndRejectsUnmanagedFiles(t *testing.T) {
	m, f := manager(t, "linux")
	f.fail = "enable --now"
	if e := m.Install(context.Background(), "daily", 9); e == nil {
		t.Fatal("registration failure hidden")
	}
	for _, path := range m.paths() {
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			t.Fatal("failed registration left schedule files")
		}
	}
	path := m.paths()[0]
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("user service"), 0600)
	if e := m.Install(context.Background(), "daily", 9); e == nil {
		t.Fatal("unmanaged file replaced")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "user service" {
		t.Fatal("unmanaged file changed")
	}
}
func TestSchedulerEscapesPathsAndRejectsInjection(t *testing.T) {
	m, _ := manager(t, "linux")
	m.Executable = filepath.Join(m.Home, `bin "$%name`)
	defs, e := m.definitions("daily", 4)
	if e != nil {
		t.Fatal(e)
	}
	text := string(defs[m.paths()[0]])
	if !strings.Contains(text, `ExecStart=:`) || !strings.Contains(text, `%%`) {
		t.Fatal(text)
	}
	m.Executable += "\nExecStart=bad"
	if _, e = m.definitions("daily", 4); e == nil {
		t.Fatal("unit injection accepted")
	}
	if _, e = m.definitions("daily", 25); e == nil {
		t.Fatal("invalid hour accepted")
	}
}
