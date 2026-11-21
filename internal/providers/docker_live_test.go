//go:build integration

package providers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

// This test never prunes an existing builder or resource. Both opt-ins are
// required, and CI runs it only on a disposable GitHub-hosted Docker daemon.
func TestLiveDockerCleanup(t *testing.T) {
	if os.Getenv("DEVCLEAN_LIVE_DOCKER") != "1" || os.Getenv("DEVCLEAN_DISPOSABLE_DOCKER") != "1" {
		t.Skip("requires explicit live/disposable Docker opt-ins")
	}
	for _, key := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "BUILDX_BUILDER"} {
		if os.Getenv(key) != "" {
			t.Fatalf("unset %s for verified local-context validation", key)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	r := runner.Exec{}
	d := Docker{Run: r}
	command := func(args ...string) string {
		t.Helper()
		out, e := r.Run(ctx, "docker", args...)
		if e != nil {
			t.Fatalf("docker %v: %v\n%s", args, e, out)
		}
		return strings.TrimSpace(out)
	}
	name, host, e := d.identity(ctx)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("Verified local Docker context %s (%s)", name, host)
	t.Log(command("version", "--format", "{{json .Server}}"))
	t.Log(command("buildx", "version"))
	token := make([]byte, 6)
	if _, e = rand.Read(token); e != nil {
		t.Fatal(e)
	}
	owner := "devclean-validation-" + hex.EncodeToString(token)
	label := "devclean.validation.owner=" + owner
	// Only exact resources created below are registered for teardown. Global
	// system/image/container/volume prune is never used, including on failure.
	cleanup := func(args ...string) {
		t.Helper()
		cleanupCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		if out, e := r.Run(cleanupCtx, "docker", args...); e != nil {
			t.Errorf("fixture teardown %v: %v %s", args, e, out)
		}
	}
	volume := owner + "-postgres-data"
	command("--context", name, "volume", "create", "--label", label, volume)
	t.Cleanup(func() { cleanup("--context", name, "volume", "rm", volume) })
	builders := []string{owner + "-one", owner + "-two"}
	for _, b := range builders {
		command("--context", name, "buildx", "create", "--name", b, "--node", b+"0", "--driver", "docker-container", name)
		t.Cleanup(func() { cleanup("--context", name, "buildx", "rm", "--force", b) })
		command("--context", name, "buildx", "inspect", b, "--bootstrap")
	}
	contextDir := t.TempDir()
	source := `package main
import("fmt";"os";"time")
func main(){if len(os.Args)>1&&os.Args[1]=="--read"{b,e:=os.ReadFile("/data/sentinel");if e!=nil{panic(e)};fmt.Print(string(b));return};if e:=os.WriteFile("/data/sentinel",[]byte("protected-fixture-data"),0600);e!=nil{panic(e)};for{time.Sleep(time.Hour)}}`
	if e = os.WriteFile(filepath.Join(contextDir, "fixture.go"), []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(contextDir, "fixture"), filepath.Join(contextDir, "fixture.go"))
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build fixture: %v %s", e, out)
	}
	payload := make([]byte, 1<<20)
	rand.Read(payload)
	os.WriteFile(filepath.Join(contextDir, "payload"), payload, 0600)
	dockerfile := "FROM scratch\nCOPY fixture /fixture\nCOPY payload /payload\nENTRYPOINT [\"/fixture\"]\n"
	os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte(dockerfile), 0600)
	images := []string{owner + ":active", owner + ":stopped", owner + ":unused"}
	for i, img := range images {
		b := builders[0]
		if i == 1 {
			b = builders[1]
		}
		if i == 2 {
			payload[0] ^= 255
			os.WriteFile(filepath.Join(contextDir, "payload"), payload, 0600)
		}
		command("--context", name, "buildx", "build", "--builder", b, "--load", "--tag", img, "--label", label, contextDir)
		t.Cleanup(func() {
			cleanupCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
			defer c()
			_, _ = r.Run(cleanupCtx, "docker", "--context", name, "image", "rm", img)
		})
	}
	active := owner + "-active"
	stopped := owner + "-stopped"
	command("--context", name, "run", "-d", "--name", active, "--label", label, "--mount", "source="+volume+",target=/data", images[0])
	t.Cleanup(func() { cleanup("--context", name, "container", "rm", "--force", active) })
	stoppedID := command("--context", name, "create", "--name", stopped, "--label", label, images[1])
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_, _ = r.Run(cleanupCtx, "docker", "--context", name, "container", "rm", stoppedID)
	})
	for tries := 0; ; tries++ {
		out, e := r.Run(ctx, "docker", "--context", name, "exec", active, "/fixture", "--read")
		if e == nil && out == "protected-fixture-data" {
			break
		}
		if tries >= 20 {
			t.Fatalf("sentinel not available: %v %s", e, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	cs, e := d.Scan(ctx)
	if e != nil {
		t.Fatal(e)
	}
	find := func(category, key, value string) storage.Candidate {
		t.Helper()
		for _, c := range cs {
			if c.Category == category && (c.Metadata[key] == value || key == "name" && c.Name == value) {
				return c
			}
		}
		t.Fatalf("candidate missing: %s %s", category, value)
		return storage.Candidate{}
	}
	one := find("BuildKit", "builder", builders[0])
	two := find("BuildKit", "builder", builders[1])
	if !one.Eligible() || !two.Eligible() || one.EstimatedBytes == 0 || two.EstimatedBytes == 0 {
		t.Fatal("multiple local builder estimates missing", one, two)
	}
	live := find("Containers", "name", active)
	vol := find("Volumes", "name", volume)
	if !live.Protected || !vol.Protected {
		t.Fatal("active resource or database volume unprotected")
	}
	for _, c := range []storage.Candidate{live, vol} {
		if _, e = d.Plan(ctx, c, 0); e == nil {
			t.Fatal("protected resource got a plan")
		}
	}
	activeImageID := command("--context", name, "image", "inspect", "--format", "{{.Id}}", images[0])
	if !find("Images", "image", activeImageID).Protected {
		t.Fatal("active image unprotected")
	}
	untouchedBefore, e := d.cacheUsage(ctx, name, builders[1])
	if e != nil {
		t.Fatal(e)
	}
	engine := storage.Engine{Home: t.TempDir(), History: storage.History{Path: filepath.Join(t.TempDir(), "history.json")}, Providers: []storage.Provider{&d}, Measure: liveDiskMeasure}
	one.Selected = true
	plan, e := engine.Plan(ctx, []storage.Candidate{one}, 0)
	var op storage.Operation
	if e == nil {
		op = plan[0]
	}
	if e != nil {
		t.Fatal(e)
	}
	records := engine.Execute(ctx, []storage.Operation{op}, func(s string) { t.Log(s) })
	if len(records) != 1 || !records[0].Success || !records[0].MeasurementValid {
		t.Fatalf("live execution/measurements failed: %+v", records)
	}
	result := records[0]
	if result.ProviderReportedBytes == nil || result.ProviderUsageBeforeBytes == nil || result.ProviderUsageAfterBytes == nil || *result.ProviderUsageBeforeBytes <= *result.ProviderUsageAfterBytes {
		t.Fatalf("live cache cleanup did not reduce usage: %+v", result)
	}
	untouchedAfter, e := d.cacheUsage(ctx, name, builders[1])
	if e != nil || untouchedBefore != untouchedAfter {
		t.Fatalf("other builder changed: %d -> %d (%v)", untouchedBefore, untouchedAfter, e)
	}
	for _, c := range []storage.Candidate{find("Containers", "container", stoppedID), find("Images", "image", command("--context", name, "image", "inspect", "--format", "{{.Id}}", images[2]))} {
		c.Selected = true
		plan, e := engine.Plan(ctx, []storage.Candidate{c}, 0)
		if e != nil {
			t.Fatal(e)
		}
		records := engine.Execute(ctx, plan, func(s string) { t.Log(s) })
		if len(records) != 1 || !records[0].Success || !records[0].MeasurementValid {
			t.Fatalf("live selected-resource cleanup failed: %+v", records)
		}
	}
	if _, e = r.Run(ctx, "docker", "--context", name, "container", "inspect", stoppedID); e == nil {
		t.Fatal("selected stopped container remains")
	}
	if _, e = r.Run(ctx, "docker", "--context", name, "image", "inspect", images[2]); e == nil {
		t.Fatal("selected unused image remains")
	}
	if out := command("--context", name, "exec", active, "/fixture", "--read"); out != "protected-fixture-data" {
		t.Fatal("persistent data changed", out)
	}
	command("--context", name, "volume", "inspect", volume)
	history, e := engine.History.Load()
	if e != nil || len(history) != 3 {
		t.Fatalf("live cleanup history missing: %+v %v", history, e)
	}
	t.Logf("Observed signed filesystem change: %d bytes; estimates and provider-reported measurements persisted separately", result.DiskDeltaBytes)
	t.Logf("LIVE VALIDATION PASSED: cache %d -> %d bytes; other builder %d -> %d; selected stopped container and unused image removed; running container/image and database volume sentinel preserved. Provider-reported bytes: %v", *result.ProviderUsageBeforeBytes, *result.ProviderUsageAfterBytes, untouchedBefore, untouchedAfter, result.ProviderReportedBytes)
}

// Tests use real host filesystem measurements even though the interactive
// application remains macOS-only. No Linux application support is enabled.
func liveDiskMeasure(path string) (storage.Disk, error) {
	var s syscall.Statfs_t
	if e := syscall.Statfs(path, &s); e != nil {
		return storage.Disk{}, e
	}
	block := uint64(s.Bsize)
	return storage.Disk{Total: s.Blocks * block, Used: (s.Blocks - s.Bfree) * block, Available: s.Bavail * block}, nil
}
