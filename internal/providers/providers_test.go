package providers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type fakeRunner struct {
	fn    func(string, []string) (string, error)
	calls []string
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.fn(name, args)
}
func clearDockerEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "BUILDX_BUILDER"} {
		t.Setenv(v, "")
	}
}
func TestDockerJSONAndCacheAccounting(t *testing.T) {
	rows, e := DecodeObjects[CacheEntry](`{"ID":"a","Size":"1000","Reclaimable":true,"Shared":false,"LastUsedAt":"2020-01-01T00:00:00Z"}
{"ID":"a","Size":1000,"Reclaimable":true}
{"ID":"shared","Size":500,"Reclaimable":true,"Shared":true}
{"ID":"busy","Size":200,"Reclaimable":false}`)
	if e != nil {
		t.Fatal(e)
	}
	total, reclaim, e := CacheBytes(rows, 30)
	if e != nil || total != 1700 || reclaim != 1000 {
		t.Fatal(total, reclaim, e)
	}
	if _, e := DecodeObjects[CacheEntry](`{bad json`); e == nil {
		t.Fatal("malformed json accepted")
	}
	rows[0].Size = []byte(`"not-bytes"`)
	if _, _, e := CacheBytes(rows, 0); e == nil {
		t.Fatal("malformed size accepted")
	}
	if n, e := ParseSize("1.5 GB"); e != nil || n != 1500000000 {
		t.Fatal(n, e)
	}
}
func dockerFake(t *testing.T) *fakeRunner {
	t.Helper()
	return &fakeRunner{fn: func(_ string, args []string) (string, error) {
		s := strings.Join(args, " ")
		switch {
		case s == "context show":
			return "desktop-linux\n", nil
		case strings.HasPrefix(s, "context inspect"):
			return `"unix:///local/docker.sock"`, nil
		case strings.Contains(s, "system df"):
			return `{"Type":"Build Cache","Size":"10MB","Reclaimable":"5MB"}`, nil
		case strings.Contains(s, "buildx ls"):
			return `{"Name":"default","Driver":"docker","Nodes":[{"Name":"default","Endpoint":"desktop-linux","Status":"running"}]}
{"Name":"alias","Driver":"docker","Nodes":[{"Name":"default","Endpoint":"desktop-linux","Status":"running"}]}
{"Name":"custom","Driver":"docker-container","Nodes":[{"Name":"custom0","Endpoint":"desktop-linux","Status":"running"}]}
{"Name":"custom","Driver":"docker-container","Nodes":[{"Name":"custom0","Endpoint":"desktop-linux","Status":"running"}]}
{"Name":"remote","Driver":"remote","Nodes":[{"Name":"r","Endpoint":"tcp://host:1234","Status":"running"}]}`, nil
		case strings.Contains(s, "buildx du"):
			return `{"ID":"cache","Size":"1000","Reclaimable":true}`, nil
		case strings.Contains(s, "ps -a") && strings.Contains(s, "ancestor="):
			return "", nil
		case strings.Contains(s, "ps -a"):
			return `{"ID":"abc123","Names":"stopped","State":"exited","Size":"1MB"}`, nil
		case strings.Contains(s, "image ls"):
			return `{"ID":"sha256:abc","Repository":"test","Tag":"latest","Size":"5MB"}`, nil
		case strings.Contains(s, "volume ls"):
			return `{"Name":"postgres-data"}`, nil
		case s == "buildx prune --help":
			return "--builder --all --force --filter", nil
		case strings.Contains(s, "buildx prune"):
			return "Total reclaimed space: 1.5GB", nil
		case strings.Contains(s, "container inspect"):
			return "running", nil
		default:
			return "", fmt.Errorf("unexpected command %s", s)
		}
	}}
}
func TestDockerMultipleBuildersProtectedVolumesAndCleanup(t *testing.T) {
	clearDockerEnv(t)
	f := dockerFake(t)
	d := Docker{Run: f}
	cs, e := d.Scan(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var custom, stopped storage.Candidate
	builders := 0
	for _, c := range cs {
		if c.Category == "BuildKit" {
			builders++
		}
		if c.Name == "custom" {
			custom = c
		}
		if c.Name == "stopped" {
			stopped = c
		}
		if c.Category == "Volumes" && (!c.Protected || c.Reclaimable) {
			t.Fatal("volume unprotected")
		}
		if c.Name == "alias" && !c.Protected {
			t.Fatal("alias cache counted twice")
		}
		if c.Name == "remote" && !c.Protected {
			t.Fatal("remote builder accepted")
		}
	}
	if builders != 4 || custom.EstimatedBytes != 1000 {
		t.Fatal(builders, custom)
	}
	if storage.Estimate(cs) != 2000 {
		t.Fatal("overlap accounting", storage.Estimate(cs))
	}
	op, e := d.Plan(context.Background(), custom, 7)
	if e != nil || !strings.Contains(op.Scope, "until=168h") {
		t.Fatal(op, e)
	}
	r, e := d.Clean(context.Background(), op)
	if e != nil || r.ProviderReportedBytes == nil || *r.ProviderReportedBytes != 1500000000 {
		t.Fatal(r, e)
	}
	op, _ = d.Plan(context.Background(), stopped, 30)
	if _, e = d.Clean(context.Background(), op); e == nil {
		t.Fatal("now-active container removed")
	}
}
func TestDockerContextChangeAndActiveImage(t *testing.T) {
	clearDockerEnv(t)
	f := dockerFake(t)
	d := Docker{Run: f}
	cs, _ := d.Scan(context.Background())
	var c storage.Candidate
	for _, v := range cs {
		if v.Category == "Images" {
			c = v
		}
	}
	op, _ := d.Plan(context.Background(), c, 30)
	old := f.fn
	f.fn = func(n string, a []string) (string, error) {
		s := strings.Join(a, " ")
		if strings.Contains(s, "ancestor=") {
			return "active-container", nil
		}
		return old(n, a)
	}
	if _, e := d.Clean(context.Background(), op); e == nil {
		t.Fatal("referenced image removed")
	}
	f.fn = func(n string, a []string) (string, error) {
		if strings.Join(a, " ") == "context show" {
			return "changed", nil
		}
		return old(n, a)
	}
	if _, e := d.Clean(context.Background(), op); e == nil {
		t.Fatal("context change ignored")
	}
	t.Setenv("DOCKER_HOST", "tcp://remote:2375")
	if _, e := d.Scan(context.Background()); e == nil {
		t.Fatal("environment override accepted")
	}
}
func TestXcodeFixtureAndDeletion(t *testing.T) {
	home := tempDir(t)
	base := filepath.Join(home, "Library/Developer/Xcode")
	project := filepath.Join(base, "DerivedData", "Project With Spaces")
	os.MkdirAll(project, 0700)
	os.WriteFile(filepath.Join(project, "cache"), make([]byte, 16384), 0600)
	device := filepath.Join(base, "iOS DeviceSupport", "26.1 (ABC123)")
	os.MkdirAll(device, 0700)
	archive := filepath.Join(base, "Archives")
	os.MkdirAll(archive, 0700)
	f := &fakeRunner{fn: func(_ string, args []string) (string, error) {
		if strings.Join(args, " ") == "-axo comm=" {
			return "/usr/bin/other", nil
		}
		return `{}`, nil
	}}
	x := Xcode{Home: home, Run: f}
	cs, e := x.Scan(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var c storage.Candidate
	for _, v := range cs {
		if v.Category == "DerivedData" {
			c = v
		}
		if v.Category == "iOS DeviceSupport" && !v.Protected {
			t.Fatal("device unprotected")
		}
		if v.Category == "Archives" && v.Reclaimable {
			t.Fatal("archive reclaimable")
		}
	}
	if !c.Eligible() || c.EstimatedBytes == 0 {
		t.Fatal(c)
	}
	op, e := x.Plan(context.Background(), c, 30)
	if e != nil {
		t.Fatal(e)
	}
	f.fn = func(string, []string) (string, error) { return "/Applications/Xcode.app/Contents/MacOS/Xcode", nil }
	if _, e := x.Clean(context.Background(), op); e == nil {
		t.Fatal("active Xcode ignored")
	}
	f.fn = func(string, []string) (string, error) { return "other", nil }
	if _, e := x.Clean(context.Background(), op); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(archive); e != nil {
		t.Fatal("archive removed")
	}
	if _, e := x.Clean(context.Background(), op); e == nil {
		t.Fatal("disappeared target accepted")
	}
}
func TestXcodeChangedDirectory(t *testing.T) {
	home := tempDir(t)
	path := filepath.Join(home, "Library/Developer/Xcode/DerivedData/project")
	os.MkdirAll(path, 0700)
	x := Xcode{Home: home, Run: &fakeRunner{fn: func(string, []string) (string, error) { return "", nil }}}
	cs, _ := x.Scan(context.Background())
	var c storage.Candidate
	for _, v := range cs {
		if v.Path == path {
			c = v
		}
	}
	op, _ := x.Plan(context.Background(), c, 0)
	os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	if _, e := x.Clean(context.Background(), op); e == nil {
		t.Fatal("changed candidate accepted")
	}
}
func TestHomebrewDryRunScope(t *testing.T) {
	root := tempDir(t)
	changed := false
	cleaned := false
	f := &fakeRunner{fn: func(_ string, a []string) (string, error) {
		s := strings.Join(a, " ")
		switch s {
		case "--prefix", "--cache":
			return root, nil
		case "cleanup --dry-run":
			if changed {
				return "changed scope", nil
			}
			return "Would remove old version\nThis operation would free approximately 2.5MB of disk space.", nil
		case "cleanup":
			cleaned = true
			return "freed 2.5MB", nil
		}
		return "", errors.New("unexpected")
	}}
	b := Homebrew{Run: f, Executable: "/fake/brew"}
	cs, e := b.Scan(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	c := cs[len(cs)-1]
	if c.EstimatedBytes != 2500000 {
		t.Fatal(c)
	}
	op, _ := b.Plan(context.Background(), c, 30)
	changed = true
	if _, e := b.Clean(context.Background(), op); e == nil || cleaned {
		t.Fatal("changed dry run executed")
	}
	changed = false
	if _, e := b.Clean(context.Background(), op); e != nil || !cleaned {
		t.Fatal(e)
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
