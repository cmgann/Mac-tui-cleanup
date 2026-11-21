//go:build integration

package providers

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

func TestLiveGlobalCacheCleanup(t *testing.T) {
	if os.Getenv("DEVCLEAN_LIVE_CACHES") != "1" {
		t.Skip("requires explicit disposable cache opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	root := tempDir(t)
	home := filepath.Join(root, "home")
	os.Mkdir(home, 0700)
	r := runner.Exec{}
	c := &Caches{Home: home, Run: r, Environment: map[string]string{"XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_DATA_HOME": filepath.Join(home, ".local/share")}}
	cacheBase := c.cacheBase()
	goBuild := filepath.Join(cacheBase, "go-build")
	goMod := filepath.Join(home, "go/pkg/mod")
	npm := filepath.Join(home, ".npm")
	pnpmHome := filepath.Join(home, ".local/share/pnpm")
	yarnBase := filepath.Join(cacheBase, "yarn")
	if runtime.GOOS == "darwin" {
		pnpmHome = filepath.Join(home, "Library/pnpm")
		yarnBase = filepath.Join(cacheBase, "Yarn")
	}
	pnpmBase := filepath.Join(pnpmHome, "store")
	swiftCache := filepath.Join(cacheBase, "org.swift.swiftpm")
	env := map[string]string{"XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_DATA_HOME": filepath.Join(home, ".local/share"), "HOME": home, "GOPATH": filepath.Join(home, "go"), "GOCACHE": goBuild, "GOMODCACHE": goMod, "GOTOOLCHAIN": "local", "GOWORK": "off", "PNPM_HOME": pnpmHome, "npm_config_store_dir": pnpmBase, "NPM_CONFIG_CACHE": npm, "npm_config_cache": npm, "YARN_CACHE_FOLDER": yarnBase, "YARN_IGNORE_PATH": "1", "NPM_CONFIG_LOGS_MAX": "0", "COREPACK_ENABLE_NETWORK": "0"}
	c.Environment = env
	neutral := filepath.Join(root, "neutral")
	os.Mkdir(neutral, 0700)
	toolCtx := runner.WithDirectory(runner.WithEnvironment(ctx, env), neutral)
	command := func(name string, args ...string) string {
		t.Helper()
		out, e := r.Run(toolCtx, name, args...)
		if e != nil {
			t.Fatalf("%s %v: %v %s", name, args, e, out)
		}
		return out
	}
	for _, name := range []string{"go", "npm", "pnpm", "yarn"} {
		if _, e := exec.LookPath(name); e != nil {
			t.Fatal("live cache validation requires " + name)
		}
	}
	// Real tool cache formats, all beneath this test's private home.
	shard := filepath.Join(goBuild, "00")
	os.MkdirAll(shard, 0700)
	goEntry := filepath.Join(shard, strings.Repeat("0", 64)+"-a")
	os.WriteFile(goEntry, make([]byte, 16384), 0600)
	module := filepath.Join(goMod, "example.invalid/fixture@v1.0.0")
	os.MkdirAll(module, 0700)
	os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.invalid/fixture\n"), 0600)
	tarPath := filepath.Join(root, "fixture.tgz")
	file, e := os.Create(tarPath)
	if e != nil {
		t.Fatal(e)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"package/package.json": `{"name":"devclean-cache-fixture","version":"1.0.0"}`, "package/index.js": "module.exports = 1;\n"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	file.Close()
	command("npm", "cache", "add", tarPath, "--offline", "--cache", npm)
	for _, tool := range []string{"pnpm", "yarn"} {
		project := filepath.Join(root, tool+"-project")
		os.Mkdir(project, 0700)
		manifest := `{"private":true,"dependencies":{"devclean-cache-fixture":"file:` + filepath.ToSlash(tarPath) + `"}}`
		os.WriteFile(filepath.Join(project, "package.json"), []byte(manifest), 0600)
		projectCtx := runner.WithDirectory(toolCtx, project)
		args := []string{"install", "--offline", "--ignore-scripts", "--store-dir", pnpmBase}
		if tool == "yarn" {
			args = []string{"install", "--offline", "--ignore-scripts", "--cache-folder", yarnBase}
		}
		out, e := r.Run(projectCtx, tool, args...)
		if e != nil {
			t.Fatalf("seed %s cache: %v %s", tool, e, out)
		}
		os.RemoveAll(project)
	}
	swiftAvailable := true
	if _, e := exec.LookPath("swift"); e != nil {
		swiftAvailable = false
		if runtime.GOOS == "darwin" {
			t.Fatal("Swift toolchain missing on macOS validation host")
		}
	}
	if swiftAvailable {
		repo := filepath.Join(swiftCache, "repositories/fixture-123456")
		os.MkdirAll(filepath.Dir(repo), 0700)
		command("git", "init", "--bare", repo)
	}
	engine := storage.Engine{Home: home, Providers: []storage.Provider{c}, History: storage.History{Path: filepath.Join(root, "history.json")}}
	cleaned := 0
	for _, spec := range cacheSpecs {
		if spec.key == "swift" && !swiftAvailable {
			t.Log("Swift unavailable on this Linux runner; native macOS job validates SwiftPM cleanup")
			continue
		}
		cacheCtx, done, e := c.context(ctx)
		if e != nil {
			t.Fatal(e)
		}
		candidate, e := c.discover(cacheCtx, spec)
		done()
		if e != nil || candidate.Protected {
			t.Fatalf("%s cache candidate: %+v %v", spec.key, candidate, e)
		}
		if !strings.HasPrefix(candidate.Path, home+string(filepath.Separator)) {
			t.Fatal("refusing non-fixture cache scope", candidate.Path)
		}
		candidate.Reviewed = true
		candidate.Selected = true
		ops, e := engine.Plan(ctx, []storage.Candidate{candidate}, 0)
		if e != nil {
			t.Fatal(spec.key, e)
		}
		rs := engine.ExecuteProgress(ctx, ops, func(event storage.ProgressEvent) {})
		if len(rs) != 1 || !rs[0].Success || !rs[0].MeasurementValid {
			t.Fatalf("%s live cleanup: %+v", spec.key, rs)
		}
		cleaned++
		t.Logf("LIVE CACHE CLEANUP PASSED: %s; usage before=%v after=%v; signed disk observation=%d", spec.key, rs[0].ProviderUsageBeforeBytes, rs[0].ProviderUsageAfterBytes, rs[0].DiskDeltaBytes)
	}
	if _, e = os.Stat(goEntry); !os.IsNotExist(e) {
		t.Fatal("Go build cache entry remains")
	}
	if _, e = os.Stat(module); !os.IsNotExist(e) {
		t.Fatal("Go module fixture remains")
	}
	if swiftAvailable {
		if _, e = os.Stat(filepath.Join(swiftCache, "repositories/fixture-123456")); !os.IsNotExist(e) {
			t.Fatal("SwiftPM repository cache fixture remains")
		}
	}
	history, e := engine.History.Load()
	if e != nil || len(history) != cleaned {
		t.Fatal("live cache history missing", history, e)
	}
	t.Logf("LIVE GLOBAL CACHE VALIDATION PASSED: %d tool-managed cleanups; only private fixture caches targeted", cleaned)
}
