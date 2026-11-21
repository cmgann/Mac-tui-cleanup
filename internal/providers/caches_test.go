package providers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

func cacheFixture(t *testing.T) (*Caches, *fakeRunner, string, *bool) {
	t.Helper()
	home := tempDir(t)
	c := &Caches{Home: home, Environment: map[string]string{"XDG_CACHE_HOME": filepath.Join(home, ".cache")}, LookPath: func(name string) (string, error) {
		if name != "go" {
			return "", errors.New("uninstalled")
		}
		return "/fixture/go", nil
	}}
	path := filepath.Join(c.cacheBase(), "go-build")
	os.MkdirAll(path, 0700)
	os.WriteFile(filepath.Join(path, "entry"), make([]byte, 16384), 0600)
	cleaned := false
	f := &fakeRunner{fn: func(_ string, args []string) (string, error) {
		switch strings.Join(args, " ") {
		case "env GOCACHE":
			return path, nil
		case "env GOMODCACHE":
			return "", os.ErrNotExist
		case "help clean":
			return "clean -cache -modcache", nil
		case "clean -cache":
			cleaned = true
			os.Remove(filepath.Join(path, "entry"))
			return "cache cleared", nil
		}
		return "", errors.New("unexpected command")
	}}
	c.Run = f
	return c, f, path, &cleaned
}
func TestGlobalCacheReviewAndSupportedCommand(t *testing.T) {
	c, _, path, cleaned := cacheFixture(t)
	ctx, done, e := c.context(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer done()
	candidate, e := c.discover(ctx, cacheSpecs[0])
	if e != nil || candidate.Protected || candidate.Eligible() {
		t.Fatal(candidate, e)
	}
	if _, e = c.Plan(context.Background(), candidate, 0); e == nil {
		t.Fatal("unreviewed cache got a cleanup plan")
	}
	candidate.Reviewed = true
	op, e := c.Plan(context.Background(), candidate, 0)
	if e != nil {
		t.Fatal(e)
	}
	result, e := c.Clean(context.Background(), op)
	if e != nil || !*cleaned || result.ProviderReportedBytes != nil {
		t.Fatal(result, e)
	}
	if _, e = os.Stat(filepath.Join(path, "entry")); !os.IsNotExist(e) {
		t.Fatal("cache fixture remains")
	}
}
func TestCacheCustomRootsRepositoriesSymlinksAndChangedPaths(t *testing.T) {
	for _, mode := range []string{"project", "symlink", "custom", "scope", "discovery-change"} {
		t.Run(mode, func(t *testing.T) {
			c, f, path, cleaned := cacheFixture(t)
			ctx, done, _ := c.context(context.Background())
			defer done()
			candidate, e := c.discover(ctx, cacheSpecs[0])
			if e != nil {
				t.Fatal(e)
			}
			candidate.Reviewed = true
			op, e := c.Plan(context.Background(), candidate, 0)
			if e != nil {
				t.Fatal(e)
			}
			old := f.fn
			switch mode {
			case "project":
				os.Mkdir(filepath.Join(path, ".git"), 0700)
			case "symlink":
				os.Symlink(t.TempDir(), filepath.Join(path, "outside"))
			case "custom", "discovery-change":
				f.fn = func(n string, a []string) (string, error) {
					if strings.Join(a, " ") == "env GOCACHE" {
						return c.Home, nil
					}
					return old(n, a)
				}
			case "scope":
				op.Args = []string{"clean", "-i", "-r"}
			}
			if _, e = c.Clean(context.Background(), op); e == nil || *cleaned {
				t.Fatal("unsafe cache cleanup accepted")
			}
		})
	}
}
func TestCacheCommandsUsePrivateDirectoryAndFrozenEnvironment(t *testing.T) {
	c, _, _, _ := cacheFixture(t)
	ctx, done, e := c.context(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer done()
	opts := runner.Options(ctx)
	actual, e := (runner.Exec{}).Run(ctx, "/bin/pwd")
	if e != nil {
		t.Fatal(e)
	}
	expected, e := filepath.EvalSymlinks(opts.Directory)
	if e != nil || strings.TrimSpace(actual) != expected {
		t.Fatal("cache command did not use private directory", actual, e)
	}
	value, e := (runner.Exec{}).Run(ctx, "/usr/bin/printenv", "GOTOOLCHAIN")
	if e != nil || strings.TrimSpace(value) != "local" {
		t.Fatal("toolchain auto-download policy not applied", value, e)
	}
}
func TestPolicyBlocksProtectedCacheDescendant(t *testing.T) {
	c, _, path, cleaned := cacheFixture(t)
	ctx, done, _ := c.context(context.Background())
	defer done()
	candidate, e := c.discover(ctx, cacheSpecs[0])
	if e != nil {
		t.Fatal(e)
	}
	candidate.Reviewed = true
	candidate.Selected = true
	engine := storage.Engine{Home: c.Home, Providers: []storage.Provider{c}, Policy: func() (storage.Policy, error) {
		return storage.Policy{Home: c.Home, ProtectedPaths: []string{filepath.Join(path, "entry")}}, nil
	}}
	if _, e = engine.Plan(context.Background(), []storage.Candidate{candidate}, 0); e == nil || *cleaned {
		t.Fatal("protected cache descendant ignored")
	}
}
