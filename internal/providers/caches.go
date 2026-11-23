package providers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type Caches struct {
	Home        string
	Run         runner.Runner
	Environment map[string]string
	LookPath    func(string) (string, error)
}
type cacheSpec struct {
	key, tool, name string
	query, help     []string
}

var cacheSpecs = []cacheSpec{
	{"go-build", "go", "Go build cache", []string{"env", "GOCACHE"}, []string{"help", "clean"}},
	{"go-modules", "go", "Go module cache", []string{"env", "GOMODCACHE"}, []string{"help", "clean"}},
	{"npm", "npm", "npm cache", []string{"config", "get", "cache"}, []string{"cache", "--help"}},
	{"pnpm", "pnpm", "pnpm store", []string{"store", "path", "--silent"}, []string{"store", "--help"}},
	{"yarn", "yarn", "Yarn Classic cache", []string{"cache", "dir"}, []string{"cache", "--help"}},
	{"swift", "swift", "Swift Package Manager cache", nil, []string{"package", "purge-cache", "--help"}},
}

func (*Caches) ID() string { return "Other Developer" }
func (c *Caches) tool(name string) (string, error) {
	find := c.LookPath
	if find == nil {
		find = exec.LookPath
	}
	p, e := find(name)
	if e != nil {
		return "", e
	}
	p, e = filepath.Abs(p)
	if e != nil {
		return "", e
	}
	return p, nil
}
func (c *Caches) context(ctx context.Context) (context.Context, func(), error) {
	dir, e := os.MkdirTemp("", "devclean-cache-tool-")
	if e != nil {
		return ctx, func() {}, e
	}
	values := map[string]string{"HOME": c.Home, "GOTOOLCHAIN": "local", "GOWORK": "off", "GOCACHEPROG": "", "COREPACK_ENABLE_NETWORK": "0", "COREPACK_ENABLE_PROJECT_SPEC": "0", "YARN_IGNORE_PATH": "1", "NPM_CONFIG_LOGS_MAX": "0"}
	for k, v := range c.Environment {
		values[k] = v
	}
	ctx = runner.WithEnvironment(ctx, values)
	ctx = runner.WithDirectory(ctx, dir)
	return ctx, func() { os.RemoveAll(dir) }, nil
}
func (c *Caches) cacheBase() string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(c.Home, "Library/Caches")
	}
	if p := c.env("XDG_CACHE_HOME"); filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Home, ".cache")
}
func (c *Caches) env(key string) string {
	if value, ok := c.Environment[key]; ok {
		return value
	}
	return os.Getenv(key)
}
func (c *Caches) discover(ctx context.Context, spec cacheSpec) (storage.Candidate, error) {
	exe, e := c.tool(spec.tool)
	if e != nil {
		return storage.Candidate{}, e
	}
	path := ""
	version := ""
	if spec.key == "yarn" || spec.key == "pnpm" {
		out, e := c.Run.Run(ctx, exe, "--version")
		if e != nil {
			return storage.Candidate{}, e
		}
		version = strings.TrimSpace(out)
		if spec.key == "yarn" && !strings.HasPrefix(version, "1.") {
			return storage.Candidate{}, errors.New("Yarn 2+ project caches remain read-only; only Yarn Classic global cleanup is supported")
		}
	}
	if spec.key == "swift" {
		path = filepath.Join(c.cacheBase(), "org.swift.swiftpm")
	} else {
		out, e := c.Run.Run(ctx, exe, spec.query...)
		if e != nil {
			return storage.Candidate{}, e
		}
		path = strings.TrimSpace(out)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return storage.Candidate{}, fmt.Errorf("%s did not return a clean absolute global-cache path", spec.name)
	}
	candidate := storage.Candidate{ID: "cache:" + spec.key + ":" + path, Provider: c.ID(), Category: "Global caches", Name: spec.name, Path: path, Risk: storage.High, Reclaimable: true, ReviewRequired: true, Metadata: map[string]string{"cache": spec.key, "tool": exe, "version": version, "age_basis": "last modified"}, ScopePaths: []string{path}}
	base, reason := c.allowedRoot(ctx, spec, path, exe)
	candidate.Metadata["base"] = base
	if spec.key == "pnpm" || spec.key == "yarn" {
		candidate.ScopePaths = []string{base}
	}
	if reason != "" {
		candidate.Protected = true
		candidate.ProtectionReason = reason
	}
	info, e := os.Lstat(path)
	if e != nil {
		return storage.Candidate{}, e
	}
	candidate.Metadata["identity"] = fileIdentity(info)
	candidate.LastModified = info.ModTime()
	n, e := storage.TreeBytes(ctx, path)
	candidate.EstimatedBytes = n
	if e != nil {
		candidate.Protected = true
		candidate.ProtectionReason = "Incomplete cache measurement: " + e.Error()
	}
	if !candidate.Protected {
		if e = c.validateTree(ctx, path, spec.key, version); e != nil {
			candidate.Protected = true
			candidate.ProtectionReason = e.Error()
		}
	}
	candidate.Description = "Global tool-managed cache only. Individual review required. Packages/build products may need downloading or rebuilding. Source repositories, project node_modules, local .build directories and arbitrary custom paths are not cleanup targets. Commands execute in a private directory with the reviewed cache location fixed explicitly."
	if spec.key == "pnpm" {
		candidate.Description += " Prune removes only packages pnpm considers unreferenced; the entire store size is an upper-bound estimate. Project registry links are not followed by the scanner. Ancillary metadata/DLX cleanup is confined to the private command directory."
	}
	if spec.key == "swift" {
		candidate.Description += " Purges SwiftPM's global repository cache; other cache entries may remain."
	}
	return candidate, nil
}
func (c *Caches) allowedRoot(ctx context.Context, s cacheSpec, path, exe string) (string, string) {
	roots := []string{}
	base := path
	switch s.key {
	case "go-build":
		roots = []string{filepath.Join(c.cacheBase(), "go-build")}
	case "go-modules":
		out, e := c.Run.Run(ctx, exe, "env", "GOPATH")
		if e != nil {
			return "", e.Error()
		}
		first := strings.Split(strings.TrimSpace(out), string(os.PathListSeparator))[0]
		if filepath.IsAbs(first) {
			roots = []string{filepath.Join(first, "pkg/mod")}
		}
	case "npm":
		roots = []string{filepath.Join(c.Home, ".npm"), filepath.Join(c.cacheBase(), "npm")}
	case "pnpm":
		store := filepath.Join(c.Home, ".local/share/pnpm/store")
		if runtime.GOOS == "darwin" {
			store = filepath.Join(c.Home, "Library/pnpm/store")
		}
		if p := c.env("XDG_DATA_HOME"); filepath.IsAbs(p) {
			store = filepath.Join(p, "pnpm/store")
		}
		if p := c.env("PNPM_HOME"); filepath.IsAbs(p) && strings.HasPrefix(p, c.Home+string(filepath.Separator)) {
			store = filepath.Join(p, "store")
		}
		if filepath.Dir(path) == store && strings.HasPrefix(filepath.Base(path), "v") {
			roots = []string{path}
			base = store
		}
	case "yarn":
		store := filepath.Join(c.cacheBase(), "yarn")
		if runtime.GOOS == "darwin" {
			store = filepath.Join(c.cacheBase(), "Yarn")
		}
		if filepath.Dir(path) == store && filepath.Base(path) == "v6" {
			roots = []string{path}
			base = store
		}
	case "swift":
		roots = []string{filepath.Join(c.cacheBase(), "org.swift.swiftpm")}
	}
	for _, root := range roots {
		if root == path {
			return base, ""
		}
	}
	return base, "Custom or project-local cache location is inspection-only; it is outside the supported global-cache policy"
}
func (c *Caches) validateTree(ctx context.Context, path, kind, version string) error {
	real, e := filepath.EvalSymlinks(path)
	if e != nil {
		return e
	}
	if real != path {
		return errors.New("symlinked cache root protected")
	}
	if path == "/" || path == filepath.Clean(c.Home) {
		return errors.New("home/filesystem root protected")
	}
	// Tool-scoped cleanup must never reach a source repository containing its cache.
	for parent := path; ; parent = filepath.Dir(parent) {
		for _, marker := range []string{".git", ".hg", ".svn", "go.mod", "package.json", "Package.swift"} {
			if _, e := os.Lstat(filepath.Join(parent, marker)); e == nil {
				return fmt.Errorf("cache is inside a source project (%s)", marker)
			} else if !errors.Is(e, os.ErrNotExist) {
				return e
			}
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	root, e := os.Stat(path)
	if e != nil {
		return e
	}
	parent, e := os.Stat(filepath.Dir(path))
	if e != nil {
		return e
	}
	if !root.IsDir() || root.Sys().(*syscall.Stat_t).Dev != parent.Sys().(*syscall.Stat_t).Dev {
		return errors.New("cache root is a special entry or unexpected mount")
	}
	return filepath.WalkDir(path, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			// pnpm 10 registers projects with pointers. Prune reads these to
			// preserve referenced packages; it only unlinks stale pointers.
			// WalkDir never follows them. Other links still fail closed.
			if kind == "pnpm" && strings.HasPrefix(version, "10.") && filepath.Dir(p) == filepath.Join(path, "projects") && registryHash(d.Name()) {
				return nil
			}
			return fmt.Errorf("cache contains symlink: %s", p)
		}
		if d.Name() == ".git" || d.Name() == ".hg" || d.Name() == ".svn" {
			return errors.New("cache contains a source repository marker")
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		if !i.IsDir() && !i.Mode().IsRegular() {
			return errors.New("cache contains a special filesystem entry")
		}
		if i.Sys().(*syscall.Stat_t).Dev != root.Sys().(*syscall.Stat_t).Dev {
			return errors.New("cache contains an unexpected mount")
		}
		return nil
	})
}
func (c *Caches) Scan(ctx context.Context) ([]storage.Candidate, error) {
	ctx, done, e := c.context(ctx)
	if e != nil {
		return nil, e
	}
	defer done()
	var cs []storage.Candidate
	var errs []error
	for _, spec := range cacheSpecs {
		candidate, e := c.discover(ctx, spec)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			errs = append(errs, fmt.Errorf("%s: %w", spec.name, e))
			continue
		}
		cs = append(cs, candidate)
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	return cs, errors.Join(errs...)
}
func cacheSpecFor(key string) (cacheSpec, bool) {
	for _, s := range cacheSpecs {
		if s.key == key {
			return s, true
		}
	}
	return cacheSpec{}, false
}
func (c *Caches) prepare(ctx context.Context, candidate storage.Candidate) (context.Context, storage.Operation, error) {
	spec, ok := cacheSpecFor(candidate.Metadata["cache"])
	if !ok || !candidate.Eligible() || !candidate.ReviewRequired || !candidate.Reviewed {
		return ctx, storage.Operation{}, errors.New("global cache requires individual review and a supported policy")
	}
	fresh, e := c.discover(ctx, spec)
	if e != nil {
		return ctx, storage.Operation{}, e
	}
	if fresh.Protected {
		return ctx, storage.Operation{}, errors.New(fresh.ProtectionReason)
	}
	for _, key := range []string{"tool", "base", "identity", "version"} {
		if fresh.Metadata[key] != candidate.Metadata[key] {
			return ctx, storage.Operation{}, fmt.Errorf("cache %s changed since review; rescan required", key)
		}
	}
	if fresh.Path != candidate.Path {
		return ctx, storage.Operation{}, errors.New("global cache location changed")
	}
	op := storage.Operation{Candidate: candidate, Warning: candidate.Description}
	exe := candidate.Metadata["tool"]
	base := candidate.Metadata["base"]
	help, e := runner.Combined(ctx, c.Run, exe, spec.help...)
	if e != nil {
		return ctx, op, e
	}
	switch spec.key {
	case "go-build", "go-modules":
		flag := "-cache"
		env := map[string]string{"GOENV": "off", "GOFLAGS": "", "GOCACHEPROG": "", "GOTOOLCHAIN": "local", "GOWORK": "off"}
		if spec.key == "go-build" {
			env["GOCACHE"] = candidate.Path
		} else {
			flag = "-modcache"
			env["GOMODCACHE"] = candidate.Path
		}
		if !strings.Contains(help, flag) {
			return ctx, op, errors.New("installed Go does not advertise selected cleanup flag")
		}
		ctx = runner.WithEnvironment(ctx, env)
		op.Args = []string{"clean", flag}
	case "npm":
		if !strings.Contains(help, "clean") {
			return ctx, op, errors.New("installed npm cache clean capability unavailable")
		}
		op.Args = []string{"cache", "clean", "--force", "--cache", candidate.Path}
	case "pnpm":
		if !strings.Contains(help, "prune") {
			return ctx, op, errors.New("installed pnpm store prune capability unavailable")
		}
		out, e := c.Run.Run(ctx, exe, "store", "path", "--silent", "--store-dir", base)
		if e != nil || strings.TrimSpace(out) != candidate.Path {
			return ctx, op, errors.New("pnpm store-dir does not resolve to the reviewed store")
		}
		// pnpm's supported --config.<name> syntax overrides settings that
		// are not accepted as command-specific flags. Verify it read-only.
		cacheDir, e := c.Run.Run(ctx, exe, "config", "get", "cache-dir", "--config.cache-dir=.")
		if e != nil || (strings.TrimSpace(cacheDir) != "." && strings.TrimSpace(cacheDir) != runner.Options(ctx).Directory) {
			return ctx, op, errors.New("pnpm cannot isolate ancillary cache pruning")
		}
		op.Args = []string{"store", "prune", "--store-dir", base, "--config.cache-dir=."}
		op.Candidate.ScopePaths = []string{base}
	case "yarn":
		if !strings.Contains(help, "clean") {
			return ctx, op, errors.New("installed Yarn Classic cache clean capability unavailable")
		}
		out, e := c.Run.Run(ctx, exe, "cache", "dir", "--cache-folder", base)
		if e != nil || strings.TrimSpace(out) != candidate.Path {
			return ctx, op, errors.New("Yarn cache-folder does not resolve to the reviewed cache")
		}
		op.Args = []string{"cache", "clean", "--cache-folder", base}
		op.Candidate.ScopePaths = []string{base}
	case "swift":
		if !strings.Contains(help, "purge-cache") || !strings.Contains(help, "--cache-path") {
			return ctx, op, errors.New("installed SwiftPM cache purge capability unavailable")
		}
		op.Args = []string{"package", "--cache-path", candidate.Path, "purge-cache"}
	}
	op.Scope = exe + " " + strings.Join(op.Args, " ") + " (global cache: " + candidate.Path + ")"
	return ctx, op, nil
}
func (c *Caches) Plan(ctx context.Context, candidate storage.Candidate, _ int) (storage.Operation, error) {
	ctx, done, e := c.context(ctx)
	if e != nil {
		return storage.Operation{}, e
	}
	defer done()
	_, op, e := c.prepare(ctx, candidate)
	return op, e
}
func (c *Caches) Clean(ctx context.Context, op storage.Operation) (storage.Result, error) {
	ctx, done, e := c.context(ctx)
	if e != nil {
		return storage.Result{}, e
	}
	defer done()
	ctx, expected, e := c.prepare(ctx, op.Candidate)
	if e != nil {
		return storage.Result{}, e
	}
	if strings.Join(expected.Args, "\x00") != strings.Join(op.Args, "\x00") {
		return storage.Result{}, errors.New("cache operation scope changed")
	}
	if e = c.validateTree(ctx, op.Candidate.Path, op.Candidate.Metadata["cache"], op.Candidate.Metadata["version"]); e != nil {
		return storage.Result{}, e
	}
	if e = storage.CheckCleanup(ctx); e != nil {
		return storage.Result{}, e
	}
	before, e := storage.TreeBytes(ctx, op.Candidate.Path)
	if e != nil {
		return storage.Result{}, e
	}
	out, e := c.Run.Run(ctx, op.Candidate.Metadata["tool"], op.Args...)
	result := storage.Result{Output: out, ProviderUsageBeforeBytes: &before}
	after, measurementErr := storage.TreeBytes(ctx, op.Candidate.Path)
	if errors.Is(measurementErr, os.ErrNotExist) {
		after = 0
		measurementErr = nil
	}
	if measurementErr == nil {
		result.ProviderUsageAfterBytes = &after
	} else {
		result.MeasurementErrors = append(result.MeasurementErrors, measurementErr.Error())
	}
	return result, e
}

// pnpm's short project hashes are 32 lowercase hexadecimal characters.
func registryHash(name string) bool {
	if len(name) != 32 {
		return false
	}
	for _, r := range name {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
