package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProtectedPathOverlapAliasesAndFutureChildren(t *testing.T) {
	home := tempDir(t)
	root := filepath.Join(home, "cache")
	os.Mkdir(root, 0700)
	os.Mkdir(filepath.Join(root, "keep"), 0700)
	link := filepath.Join(home, "alias")
	os.Symlink(root, link)
	c := Candidate{ID: "cache", Path: root, Reclaimable: true}
	for _, path := range []string{root, home, filepath.Join(root, "keep"), filepath.Join(link, "future", "symbols"), "~/cache/keep"} {
		p := Policy{Home: home, ProtectedPaths: []string{path}}
		if p.Check(c) == nil {
			t.Fatalf("protected overlap accepted: %s", path)
		}
	}
	if (Policy{Home: home, ProtectedPaths: []string{filepath.Join(home, "cache-other")}}).Check(c) != nil {
		t.Fatal("sibling prefix overmatched")
	}
	if (Policy{Home: home, ProtectedPaths: []string{"relative"}}).Check(c) == nil {
		t.Fatal("relative protection ignored")
	}
	virtual := Candidate{ID: "brew", ScopePaths: []string{root}}
	if (Policy{Home: home, ProtectedPaths: []string{filepath.Join(root, "keep")}}).Check(virtual) == nil {
		t.Fatal("command footprint ignored")
	}
}
func TestStaleHintsNeverSelectUnknownDatesOrProtectedItems(t *testing.T) {
	now := time.Now()
	cs := []Candidate{{ID: "old", Reclaimable: true, LastModified: now.Add(-100 * 24 * time.Hour)}, {ID: "recent", Reclaimable: true, LastModified: now}, {ID: "unknown", Reclaimable: true}, {ID: "pinned", Reclaimable: true, LastModified: now.Add(-100 * 24 * time.Hour)}}
	p := Policy{StaleDays: 90, Pinned: []string{"pinned"}}
	p.Apply(cs, now)
	if !cs[0].Stale || cs[0].StaleReason == "" || cs[0].Selected {
		t.Fatal(cs[0])
	}
	for _, c := range cs[1:] {
		if c.Stale || c.Selected {
			t.Fatal(c)
		}
	}
	if !cs[3].Protected {
		t.Fatal("pin ignored")
	}
}
func TestProtectionAddedAfterReviewStopsExecution(t *testing.T) {
	root := tempDir(t)
	path := filepath.Join(root, "cache")
	os.Mkdir(path, 0700)
	p := &fakeProvider{}
	protected := false
	e := Engine{Home: root, History: History{Path: filepath.Join(tempDir(t), "history.json")}, Providers: []Provider{p}, Policy: func() (Policy, error) {
		if protected {
			return Policy{Home: root, ProtectedPaths: []string{path}}, nil
		}
		return Policy{Home: root}, nil
	}}
	c := Candidate{ID: "one", Provider: "fake", Path: path, Reclaimable: true, Selected: true}
	ops, err := e.Plan(context.Background(), []Candidate{c}, 30)
	if err != nil {
		t.Fatal(err)
	}
	protected = true
	rs := e.Execute(context.Background(), ops, func(string) {})
	if p.calls != 0 || len(rs) != 1 || rs[0].Success {
		t.Fatal("stale plan bypassed protection", rs)
	}
	e.Policy = func() (Policy, error) { return Policy{}, errors.New("corrupt policy") }
	if _, err = e.Plan(context.Background(), []Candidate{c}, 30); err == nil {
		t.Fatal("unavailable protection policy ignored")
	}
}
func TestCleanupGuardRunsAtFilesystemBoundary(t *testing.T) {
	root := tempDir(t)
	path := filepath.Join(root, "project")
	os.Mkdir(path, 0700)
	os.WriteFile(filepath.Join(path, "symbols"), []byte("keep"), 0600)
	ctx := WithCleanupGuard(context.Background(), func() error { return errors.New("new protection") })
	if DeleteChild(ctx, root, path) == nil {
		t.Fatal("filesystem guard bypassed")
	}
	if _, e := os.Stat(filepath.Join(path, "symbols")); e != nil {
		t.Fatal("guard changed data")
	}
}
