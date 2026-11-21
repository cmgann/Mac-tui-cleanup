//go:build integration && darwin

package providers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

// Only exact devices created in this private set are deleted. Installed runtime
// images and the runner's default simulator set are never cleanup targets.
func TestLiveSimulatorCleanup(t *testing.T) {
	if os.Getenv("DEVCLEAN_LIVE_SIMULATOR") != "1" {
		t.Skip("requires explicit disposable simulator opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r := runner.Exec{}
	home, e := os.UserHomeDir()
	if e != nil {
		t.Fatal(e)
	}
	root := tempDir(t)
	set := filepath.Join(root, "Devices")
	if e = os.Mkdir(set, 0700); e != nil {
		t.Fatal(e)
	}
	s := Simulators{Home: home, Run: r, DeviceSet: set}
	tool, e := s.tool(ctx)
	if e != nil {
		t.Fatal(e)
	}
	command := func(args ...string) string {
		t.Helper()
		out, e := r.Run(ctx, tool, args...)
		if e != nil {
			t.Fatalf("simctl %v: %v %s", args, e, out)
		}
		return out
	}
	help, e := runner.Combined(ctx, r, tool, "help", "delete")
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("Installed delete capability: %s", help)
	profiles, e := s.profiles(ctx, tool)
	if e != nil {
		t.Fatal(e)
	}
	runtime := ""
	for _, p := range profiles {
		if p.IsAvailable != nil && *p.IsAvailable && strings.Contains(p.Identifier, ".iOS-") {
			runtime = p.Identifier
			break
		}
	}
	if runtime == "" {
		t.Fatal("live validation requires an installed available iOS runtime")
	}
	var types struct{ DeviceTypes []struct{ Identifier string } }
	if e = json.Unmarshal([]byte(command("list", "devicetypes", "--json")), &types); e != nil {
		t.Fatal(e)
	}
	token := make([]byte, 6)
	if _, e = rand.Read(token); e != nil {
		t.Fatal(e)
	}
	name := "devclean-validation-" + hex.EncodeToString(token)
	create := func(label string) string {
		t.Helper()
		for i := len(types.DeviceTypes) - 1; i >= 0; i-- {
			kind := types.DeviceTypes[i].Identifier
			if !strings.Contains(kind, ".iPhone-") {
				continue
			}
			out, e := r.Run(ctx, tool, "--set", set, "create", label, kind, runtime)
			if e != nil {
				continue
			}
			id := strings.TrimSpace(out)
			if !simulatorUUID.MatchString(id) {
				t.Fatalf("invalid created simulator ID %q", id)
			}
			t.Cleanup(func() {
				cleanupCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
				defer c()
				_, _ = r.Run(cleanupCtx, tool, "--set", set, "delete", id)
			})
			return id
		}
		t.Fatal("no supported iPhone device type could be created for " + runtime)
		return ""
	}
	selected := create(name + "-selected")
	preserved := create(name + "-preserved")
	cs, scanErr := s.Scan(ctx)
	if scanErr != nil {
		t.Logf("Managed runtime inspection warning (device cleanup remains independent): %v", scanErr)
	}
	var c storage.Candidate
	for _, candidate := range cs {
		if candidate.Metadata["resource"] == selected {
			c = candidate
		}
	}
	if c.ID == "" || c.Protected || !c.ReviewRequired || c.Eligible() {
		t.Fatalf("unexpected live simulator candidate: %+v", c)
	}
	c.Reviewed = true
	c.Selected = true
	engine := storage.Engine{Home: home, History: storage.History{Path: filepath.Join(root, "history.json")}, Providers: []storage.Provider{&s}}
	ops, e := engine.Plan(ctx, []storage.Candidate{c}, 0)
	if e != nil {
		t.Fatal(e)
	}
	outputEvents := 0
	records := engine.ExecuteProgress(ctx, ops, func(event storage.ProgressEvent) {
		if event.Kind == "output" {
			outputEvents++
		}
		if event.Kind == "operation" || event.Kind == "finished" {
			t.Logf("%s: %s (%d/%d)", event.Kind, event.Name, event.Completed, event.Total)
		}
	})
	if len(records) != 1 || !records[0].Success || !records[0].MeasurementValid {
		t.Fatalf("live simulator cleanup failed: %+v", records)
	}
	rows, e := s.devices(ctx, tool)
	if e != nil {
		t.Fatal(e)
	}
	preservedFound := false
	for _, d := range rows {
		if d.UDID == selected {
			t.Fatal("selected simulator still present")
		}
		if d.UDID == preserved {
			preservedFound = true
		}
	}
	if !preservedFound {
		t.Fatal("other simulator was removed")
	}
	history, e := engine.History.Load()
	if e != nil || len(history) != 1 || history[0].CommandOutput == "" || outputEvents == 0 {
		t.Fatal("live simulator progress/history missing", history, e, outputEvents)
	}
	t.Logf("LIVE SIMULATOR VALIDATION PASSED: selected device deleted via simctl; second device preserved; private device set %s; signed disk change %d; %d live output events captured. No runtime image deleted.", set, records[0].DiskDeltaBytes, outputEvents)
	// Probe current installed runtime JSON read-only, so compatibility failures are
	// visible in CI without ever downloading or deleting a system runtime.
	helpRuntime, e := runner.Combined(ctx, r, tool, "help", "runtime")
	if e != nil || !strings.Contains(helpRuntime, "delete") || !strings.Contains(helpRuntime, "--dry-run") {
		t.Fatalf("installed runtime deletion/dry-run capability unavailable: %v\n%s", e, helpRuntime)
	}
	images, e := s.images(ctx, tool)
	if e != nil {
		t.Fatal(e)
	}
	probeID := ""
	for _, image := range images {
		if image.Deletable != nil && *image.Deletable && managedRuntimeKind(image.Kind) {
			probeID = image.Identifier
			break
		}
	}
	if probeID == "" {
		t.Fatal("live runtime capability validation requires a managed deletable runtime image")
	}
	dry, e := runner.Combined(ctx, r, tool, "--set", set, "runtime", "delete", probeID, "--dry-run")
	if e != nil {
		t.Fatalf("installed runtime dry-run failed: %v %s", e, dry)
	}
	after, e := s.images(ctx, tool)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, image := range after {
		found = found || image.Identifier == probeID
	}
	if !found {
		t.Fatal("dry-run unexpectedly removed a runtime image")
	}
	t.Logf("LIVE RUNTIME CAPABILITY VALIDATION PASSED: managed-image JSON recognized; target-specific dry-run supported; runtime still present. Dry-run output: %s", dry)
}
