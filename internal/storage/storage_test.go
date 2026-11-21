package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFormattingAndAccounting(t *testing.T) {
	for n, want := range map[uint64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 1500000: "1.5 MB"} {
		if got := Bytes(n); got != want {
			t.Fatalf("%d: %s != %s", n, got, want)
		}
	}
	if Delta(100, 50) != -50 || Delta(50, 100) != 50 {
		t.Fatal("delta must be signed")
	}
	if Signed(-1000) != "−1.0 kB" {
		t.Fatal(Signed(-1000))
	}
	if (Disk{}).Percent() != 0 {
		t.Fatal("zero total")
	}
	cs := []Candidate{{ID: "a", Reclaimable: true, EstimatedBytes: 100, Metadata: map[string]string{"accounting": "shared"}}, {ID: "b", Reclaimable: true, EstimatedBytes: 100, Metadata: map[string]string{"accounting": "shared"}}, {ID: "c", Reclaimable: true, Protected: true, EstimatedBytes: 1000}, {ID: "d", EstimatedBytes: 2000}}
	if Estimate(cs) != 100 {
		t.Fatal("double counted or protected bytes included")
	}
}
func TestPathSafety(t *testing.T) {
	root := tempDir(t)
	good := filepath.Join(root, "Project With Spaces")
	if e := os.Mkdir(good, 0700); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(good, "cache"), []byte("payload"), 0600)
	ctx := context.Background()
	if e := ValidateTarget(ctx, root, good); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{root, "/", filepath.Join(root, ".."), good + "/../Project With Spaces", filepath.Join(root, "missing")} {
		if e := ValidateTarget(ctx, root, path); e == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
	outside := tempDir(t)
	sentinel := filepath.Join(outside, "important")
	os.WriteFile(sentinel, []byte("safe"), 0600)
	os.Symlink(outside, filepath.Join(good, "link"))
	if e := DeleteChild(ctx, root, good); e == nil {
		t.Fatal("symlink accepted")
	}
	os.Remove(filepath.Join(good, "link"))
	if _, e := os.Stat(sentinel); e != nil {
		t.Fatal("outside data changed")
	}
	os.Mkdir(filepath.Join(good, ".git"), 0700)
	if e := DeleteChild(ctx, root, good); e == nil {
		t.Fatal("repository accepted")
	}
	os.Remove(filepath.Join(good, ".git"))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if e := DeleteChild(canceled, root, good); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e := DeleteChild(ctx, root, good); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(good); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("not removed")
	}
	if e := DeleteChild(ctx, root, good); e == nil {
		t.Fatal("disappearing target accepted")
	}
}
func TestSymlinkRootAndSpecialFiles(t *testing.T) {
	root := tempDir(t)
	real := filepath.Join(root, "real")
	os.Mkdir(real, 0700)
	os.Mkdir(filepath.Join(real, "child"), 0700)
	link := filepath.Join(root, "link")
	os.Symlink(real, link)
	if ValidateTarget(context.Background(), link, filepath.Join(link, "child")) == nil {
		t.Fatal("symlink root accepted")
	}
}
func TestHistoryPersistence(t *testing.T) {
	h := History{Path: filepath.Join(tempDir(t), "history.json")}
	r := Record{ID: "one", Timestamp: time.Now(), MeasurementValid: true, DiskDeltaBytes: -42}
	if e := h.Append(r); e != nil {
		t.Fatal(e)
	}
	rows, e := h.Load()
	if e != nil || len(rows) != 1 || Lifetime(rows) != -42 {
		t.Fatalf("%v %v", rows, e)
	}
	i, _ := os.Stat(h.Path)
	if i.Mode().Perm() != 0600 {
		t.Fatal("history permissions")
	}
	os.WriteFile(h.Path, []byte("broken json"), 0600)
	if _, e := h.Load(); e == nil {
		t.Fatal("corrupt history silently accepted")
	}
	if e := h.Append(r); e == nil {
		t.Fatal("corrupt history overwritten")
	}
	b, _ := os.ReadFile(h.Path)
	if string(b) != "broken json" {
		t.Fatal("corrupt history changed")
	}
}
func TestLock(t *testing.T) {
	path := filepath.Join(tempDir(t), "lock")
	unlock, e := FileLock(path)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	if u, e := FileLock(path); e == nil {
		u()
		t.Fatal("concurrent lock accepted")
	}
}

type fakeProvider struct {
	calls int
	fail  bool
}

func (*fakeProvider) ID() string { return "fake" }
func (*fakeProvider) Scan(context.Context) ([]Candidate, error) {
	return nil, errors.New("provider unavailable")
}
func (*fakeProvider) Plan(_ context.Context, c Candidate, _ int) (Operation, error) {
	return Operation{Candidate: c, Scope: "fake clean"}, nil
}
func (p *fakeProvider) Clean(ctx context.Context, _ Operation) (Result, error) {
	p.calls++
	if p.fail {
		return Result{}, errors.New("partial removal")
	}
	return Result{Output: "done"}, nil
}
func TestPlanningAndPartialExecution(t *testing.T) {
	p := &fakeProvider{fail: true}
	e := Engine{Home: tempDir(t), History: History{Path: filepath.Join(tempDir(t), "history.json")}, Providers: []Provider{p}}
	c := Candidate{ID: "x", Provider: "fake", Selected: true, Reclaimable: true, EstimatedBytes: 10}
	if _, err := e.Plan(context.Background(), []Candidate{{Selected: true, Protected: true}}, 30); err == nil {
		t.Fatal("protected selection accepted")
	}
	if _, err := e.Plan(context.Background(), nil, 30); err == nil {
		t.Fatal("empty plan accepted")
	}
	ops, err := e.Plan(context.Background(), []Candidate{c, c}, 30)
	if err != nil || len(ops) != 1 {
		t.Fatal(ops, err)
	}
	rs := e.Execute(context.Background(), ops, func(string) {})
	if len(rs) != 1 || rs[0].Success || !strings.Contains(strings.Join(rs[0].Errors, " "), "partial removal") {
		t.Fatal(rs)
	}
	saved, _ := e.History.Load()
	if len(saved) != 1 {
		t.Fatal("partial result not persisted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := e.Execute(ctx, ops, func(string) {}); len(got) != 0 {
		t.Fatal("canceled cleanup ran")
	}
	if p.calls != 1 {
		t.Fatal("wrong calls")
	}
	snap := e.Scan(context.Background())
	if len(snap.Errors) == 0 {
		t.Fatal("provider failure hidden")
	}
}

func tempDir(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestObservedChangeWithFailedOperation(t *testing.T) {
	p := &fakeProvider{fail: true}
	calls := 0
	e := Engine{Home: tempDir(t), History: History{Path: filepath.Join(tempDir(t), "history.json")}, Providers: []Provider{p}, Measure: func(string) (Disk, error) {
		calls++
		if calls == 1 {
			return Disk{Available: 1000}, nil
		}
		return Disk{Available: 900}, nil
	}}
	rs := e.Execute(context.Background(), []Operation{{Candidate: Candidate{Provider: "fake", EstimatedBytes: 5000}}}, func(string) {})
	if len(rs) != 1 || rs[0].Success || rs[0].DiskDeltaBytes != -100 || rs[0].EstimatedBytes != 5000 || rs[0].ProviderReportedBytes != nil {
		t.Fatal(rs)
	}
}

func TestReadOnlyDirectoryAndReplacedTarget(t *testing.T) {
	root := tempDir(t)
	path := filepath.Join(root, "project")
	os.Mkdir(path, 0700)
	os.WriteFile(filepath.Join(path, "cache"), []byte("cache"), 0600)
	original, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	os.Rename(path, path+"-old")
	os.Mkdir(path, 0700)
	os.WriteFile(filepath.Join(path, "new"), []byte("do not remove"), 0600)
	if e := DeleteChild(context.Background(), root, path, original); e == nil {
		t.Fatal("replaced target accepted")
	}
	if _, e := os.Stat(filepath.Join(path, "new")); e != nil {
		t.Fatal("replacement data removed")
	}
	os.Chmod(path, 0500)
	defer os.Chmod(path, 0700)
	if e := DeleteChild(context.Background(), root, path); e == nil {
		t.Fatal("read-only directory removal unexpectedly succeeded")
	}
}

func TestMountDeviceGuard(t *testing.T) {
	root := tempDir(t)
	path := filepath.Join(root, "project")
	os.Mkdir(path, 0700)
	r, e := os.OpenRoot(root)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	i, e := r.Stat(".")
	if e != nil {
		t.Fatal(e)
	}
	wrongDevice := uint64(i.Sys().(*syscall.Stat_t).Dev) + 1
	if e = removeTree(context.Background(), r, "project", wrongDevice); e == nil {
		t.Fatal("device boundary ignored")
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("mounted target was removed")
	}
}

func TestDeletionBoundaryGuard(t *testing.T) {
	root := tempDir(t)
	path := filepath.Join(root, "project")
	os.Mkdir(path, 0700)
	sentinel := filepath.Join(path, "symbols")
	os.WriteFile(sentinel, []byte("preserve"), 0600)
	info, _ := os.Stat(path)
	called := false
	err := DeleteChildGuarded(context.Background(), root, path, info, func(context.Context) error { called = true; return errors.New("device attached after validation") })
	if err == nil || !called {
		t.Fatal("boundary eligibility guard bypassed")
	}
	if _, e := os.Stat(sentinel); e != nil {
		t.Fatal("guard rejection changed data")
	}
}
