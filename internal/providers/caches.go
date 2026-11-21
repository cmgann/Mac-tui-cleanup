package providers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type Caches struct {
	Home string
	Run  runner.Runner
}

func (*Caches) ID() string { return "Other Developer" }
func (c *Caches) Scan(ctx context.Context) ([]storage.Candidate, error) {
	paths := map[string]string{"Swift Package Manager": filepath.Join(c.Home, "Library/Caches/org.swift.swiftpm")}
	var errs []error
	for _, cmd := range []struct {
		name  string
		args  []string
		label string
	}{{"go", []string{"env", "GOCACHE"}, "Go build cache"}, {"go", []string{"env", "GOMODCACHE"}, "Go module cache"}, {"npm", []string{"config", "get", "cache"}, "npm cache"}, {"pnpm", []string{"store", "path", "--silent"}, "pnpm store"}, {"yarn", []string{"cache", "dir"}, "Yarn cache"}} {
		out, e := c.Run.Run(ctx, cmd.name, cmd.args...)
		if e != nil {
			errs = append(errs, e)
			continue
		}
		path := strings.TrimSpace(out)
		if filepath.IsAbs(path) && !strings.Contains(path, "\n") {
			paths[cmd.label] = path
		} else {
			errs = append(errs, errors.New(cmd.label+": tool returned no absolute cache path"))
		}
	}
	var cs []storage.Candidate
	for label, path := range paths {
		if _, e := os.Lstat(path); errors.Is(e, os.ErrNotExist) {
			continue
		}
		n, e := storage.TreeBytes(ctx, path)
		if e != nil {
			errs = append(errs, e)
		}
		cs = append(cs, storage.Candidate{ID: "cache:" + path, Provider: c.ID(), Category: "Caches", Name: label, Path: path, EstimatedBytes: n, Protected: true, ProtectionReason: "Read-only analysis; cache cleanup must use a supported tool-specific policy"})
	}
	return cs, errors.Join(errs...)
}
func (*Caches) Plan(context.Context, storage.Candidate, int) (storage.Operation, error) {
	return storage.Operation{}, errors.New("additional developer caches are read-only")
}
func (*Caches) Clean(context.Context, storage.Operation) (storage.Result, error) {
	return storage.Result{}, errors.New("additional developer caches are read-only")
}
