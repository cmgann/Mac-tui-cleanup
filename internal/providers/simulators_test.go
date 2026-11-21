package providers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

const simID = "11111111-2222-3333-4444-555555555555"
const imageID = "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"
const simRuntime = "com.apple.CoreSimulator.SimRuntime.iOS-18-0"

func simFixture(t *testing.T) (*Simulators, *fakeRunner, *SimDevice, *SimImage, *bool) {
	t.Helper()
	home := tempDir(t)
	root := filepath.Join(home, "Library/Developer/CoreSimulator/Devices")
	path := filepath.Join(root, simID)
	os.MkdirAll(filepath.Join(path, "data"), 0700)
	os.WriteFile(filepath.Join(path, "data", "fixture"), make([]byte, 16384), 0600)
	available := false
	deletable := true
	removed := false
	d := &SimDevice{UDID: simID, Name: "Unavailable fixture", State: "Shutdown", DataPath: filepath.Join(path, "data"), IsAvailable: &available}
	image := &SimImage{Identifier: imageID, Build: "22A100", Version: "18.0", Kind: "Disk Image", State: "Ready", RuntimeIdentifier: simRuntime, Deletable: &deletable, SizeBytes: json.RawMessage(`1000000`)}
	f := &fakeRunner{fn: func(name string, args []string) (string, error) {
		s := strings.Join(args, " ")
		switch {
		case name == "xcrun":
			return "/fixture/simctl", nil
		case name == "/bin/ps":
			return "other", nil
		case s == "help delete":
			return "Usage: simctl delete <device>", nil
		case s == "help runtime":
			return "runtime delete <identifier> --dry-run", nil
		case strings.Contains(s, "list devices --json"):
			rows := []SimDevice{}
			if !removed {
				rows = append(rows, *d)
			}
			b, _ := json.Marshal(map[string]any{"devices": map[string]any{simRuntime: rows}})
			return string(b), nil
		case strings.Contains(s, "list runtimes --json"):
			return `{"runtimes":[{"identifier":"` + simRuntime + `","name":"iOS 18","version":"18.0","buildversion":"22A100","isAvailable":true}]}`, nil
		case strings.Contains(s, "runtime list --json"):
			rows := map[string]SimImage{}
			if image.Identifier != "" {
				rows[image.Identifier] = *image
			}
			b, _ := json.Marshal(rows)
			return string(b), nil
		case strings.Contains(s, "runtime delete "+imageID+" --dry-run"):
			return "Would delete " + imageID, nil
		case strings.Contains(s, "runtime delete "+imageID):
			image.Identifier = ""
			return "Runtime removed", nil
		case strings.Contains(s, "delete "+simID):
			removed = true
			return "Simulator removed", nil
		}
		return "", errors.New("unexpected command: " + name + " " + s)
	}}
	return &Simulators{Home: home, Run: f}, f, d, image, &removed
}
func TestSimulatorDeviceReviewAndFreshChecks(t *testing.T) {
	s, _, device, _, removed := simFixture(t)
	cs, e := s.Scan(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var candidate storage.Candidate
	for _, c := range cs {
		if c.Category == simulatorDevices {
			candidate = c
		}
	}
	if candidate.Protected || candidate.Eligible() || !candidate.ReviewRequired || candidate.EstimatedBytes == 0 {
		t.Fatal(candidate)
	}
	if _, e = s.Plan(context.Background(), candidate, 0); e == nil {
		t.Fatal("unreviewed simulator accepted")
	}
	candidate.Reviewed = true
	op, e := s.Plan(context.Background(), candidate, 0)
	if e != nil {
		t.Fatal(e)
	}
	device.State = "Booted"
	if _, e = s.Clean(context.Background(), op); e == nil || *removed {
		t.Fatal("booted simulator removed")
	}
	device.State = "Shutdown"
	if _, e = s.Clean(context.Background(), op); e != nil || !*removed {
		t.Fatal("reviewed simulator not removed", e)
	}
	for _, call := range s.Run.(*fakeRunner).calls {
		if strings.HasSuffix(call, "delete all") || strings.HasSuffix(call, "delete unavailable") {
			t.Fatal("bulk simctl deletion used")
		}
	}
}
func TestSimulatorUnsafeScopeAndToolchain(t *testing.T) {
	for _, mode := range []string{"path", "symlink", "missing-availability", "malformed-json", "toolchain", "tampered-args", "capability", "changed-runtime"} {
		t.Run(mode, func(t *testing.T) {
			s, f, d, _, removed := simFixture(t)
			cs, _ := s.Scan(context.Background())
			c := cs[0]
			c.Reviewed = true
			op, e := s.Plan(context.Background(), c, 0)
			if e != nil {
				t.Fatal(e)
			}
			old := f.fn
			switch mode {
			case "path":
				d.DataPath = filepath.Join(t.TempDir(), "documents")
			case "symlink":
				os.Rename(c.Path, c.Path+"-old")
				os.Symlink(c.Path+"-old", c.Path)
			case "missing-availability":
				d.IsAvailable = nil
			case "malformed-json":
				f.fn = func(n string, a []string) (string, error) {
					if strings.Contains(strings.Join(a, " "), "list devices") {
						return `{}`, nil
					}
					return old(n, a)
				}
			case "toolchain":
				f.fn = func(n string, a []string) (string, error) {
					if n == "xcrun" {
						return "/other/simctl", nil
					}
					return old(n, a)
				}
			case "tampered-args":
				op.Args = []string{"delete", "all"}
			case "capability":
				f.fn = func(n string, a []string) (string, error) {
					if strings.Join(a, " ") == "help delete" {
						return "", errors.New("unsupported")
					}
					return old(n, a)
				}
			case "changed-runtime":
				f.fn = func(n string, a []string) (string, error) {
					out, e := old(n, a)
					if strings.Contains(strings.Join(a, " "), "list devices") {
						out = strings.ReplaceAll(out, simRuntime, "com.apple.CoreSimulator.SimRuntime.iOS-19-0")
					}
					return out, e
				}
			}
			if _, e = s.Clean(context.Background(), op); e == nil || *removed {
				t.Fatal("unsafe simulator cleanup accepted")
			}
		})
	}
}
func TestUnusedRuntimeDeleteAndReferenceProtection(t *testing.T) {
	s, _, d, image, removed := simFixture(t)
	cs, e := s.Scan(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range cs {
		if c.Category == simulatorRuntimes && !c.Protected {
			t.Fatal("referenced runtime not protected")
		}
	}
	*removed = true
	cs, e = s.Scan(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var c storage.Candidate
	for _, v := range cs {
		if v.Category == simulatorRuntimes {
			c = v
		}
	}
	c.Reviewed = true
	op, e := s.Plan(context.Background(), c, 0)
	if e != nil || !strings.Contains(op.Warning, "Dry run") {
		t.Fatal(op, e)
	}
	*removed = false
	d.State = "Booted"
	if _, e = s.Clean(context.Background(), op); e == nil || image.Identifier == "" {
		t.Fatal("new runtime reference not protected")
	}
	*removed = true
	if _, e = s.Clean(context.Background(), op); e != nil || image.Identifier != "" {
		t.Fatal("unused runtime not removed", e)
	}
}
func TestRuntimeRequiresExplicitDeletabilityAndDryRun(t *testing.T) {
	for _, mode := range []string{"unknown-deletability", "not-deletable", "unsupported-kind", "ambiguous-profile", "dry-run-failure", "resource-remains"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, image, removed := simFixture(t)
			*removed = true
			old := f.fn
			switch mode {
			case "unknown-deletability":
				image.Deletable = nil
			case "not-deletable":
				v := false
				image.Deletable = &v
			case "unsupported-kind":
				image.Kind = "Bundle"
			case "ambiguous-profile":
				image.RuntimeIdentifier = "unknown"
			case "dry-run-failure":
				f.fn = func(n string, a []string) (string, error) {
					if strings.Contains(strings.Join(a, " "), "--dry-run") {
						return "", errors.New("runtime busy")
					}
					return old(n, a)
				}
			case "resource-remains":
				f.fn = func(n string, a []string) (string, error) {
					if strings.HasSuffix(strings.Join(a, " "), "runtime delete "+imageID) {
						return "", nil
					}
					return old(n, a)
				}
			}
			cs, e := s.Scan(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			c := cs[0]
			c.Reviewed = true
			op, e := s.Plan(context.Background(), c, 0)
			if mode == "resource-remains" {
				if e != nil {
					t.Fatal(e)
				}
				if _, e = s.Clean(context.Background(), op); e == nil {
					t.Fatal("unverified deletion treated as success")
				}
			} else if e == nil {
				t.Fatal("unsafe runtime deletion planned")
			}
		})
	}
}

func TestSimulatorActivityDuringCapabilityCheckBlocksDeletion(t *testing.T) {
	s, f, d, _, removed := simFixture(t)
	cs, _ := s.Scan(context.Background())
	c := cs[0]
	c.Reviewed = true
	op, e := s.Plan(context.Background(), c, 0)
	if e != nil {
		t.Fatal(e)
	}
	old := f.fn
	f.fn = func(n string, a []string) (string, error) {
		out, e := old(n, a)
		if strings.Join(a, " ") == "help delete" {
			d.State = "Booted"
		}
		return out, e
	}
	if _, e = s.Clean(context.Background(), op); e == nil || *removed {
		t.Fatal("device booted during capability check but was deleted")
	}
}

func TestPatchableCryptexRuntimeUsesSupportedCleanup(t *testing.T) {
	s, _, _, image, removed := simFixture(t)
	*removed = true
	image.Kind = "Patchable Cryptex Disk Image"
	cs, e := s.Scan(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(cs) != 1 || cs[0].Protected {
		t.Fatal("managed Cryptex image was not recognized", cs)
	}
	c := cs[0]
	c.Reviewed = true
	op, e := s.Plan(context.Background(), c, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Clean(context.Background(), op); e != nil || image.Identifier != "" {
		t.Fatal("supported Cryptex runtime cleanup failed", e)
	}
}
