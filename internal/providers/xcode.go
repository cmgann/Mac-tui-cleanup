package providers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type Xcode struct {
	Home                string
	Run                 runner.Runner
	SystemSimulatorRoot string
}

func (*Xcode) ID() string { return "Xcode" }
func (x *Xcode) derivedRoot() string {
	return filepath.Join(x.Home, "Library/Developer/Xcode/DerivedData")
}
func fileIdentity(i os.FileInfo) string {
	st := i.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d:%d", st.Dev, st.Ino, i.ModTime().UnixNano())
}
func (x *Xcode) Scan(ctx context.Context) ([]storage.Candidate, error) {
	var cs []storage.Candidate
	var errs []error
	base := filepath.Join(x.Home, "Library/Developer/Xcode")
	for _, category := range []string{"DerivedData", "iOS DeviceSupport", "watchOS DeviceSupport", "tvOS DeviceSupport", "visionOS DeviceSupport", "macOS DeviceSupport"} {
		parent := filepath.Join(base, category)
		entries, e := os.ReadDir(parent)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			errs = append(errs, e)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(parent, entry.Name())
			i, e := entry.Info()
			if e != nil {
				errs = append(errs, e)
				continue
			}
			n, e := storage.TreeBytes(ctx, path)
			c := storage.Candidate{ID: "xcode:" + path, Provider: x.ID(), Category: category, Name: entry.Name(), Path: path, EstimatedBytes: n, LastModified: i.ModTime(), Risk: storage.Medium, Metadata: map[string]string{"identity": fileIdentity(i)}}
			if category == "DerivedData" {
				c.Reclaimable = true
				c.Description = "Project build products and indexes. Xcode regenerates these. Close Xcode and xcodebuild before cleaning."
				if e := storage.ValidateTarget(ctx, parent, path); e != nil {
					c.Protected = true
					c.ProtectionReason = e.Error()
				}
			} else {
				c.Protected = true
				c.ProtectionReason = "Device support protected: connected-device identity cannot be reliably established by this release. Historical files may be needed to symbolize crash reports."
				c.Description = "Platform: " + category + " · Version/build from directory: " + entry.Name() + " · Device identifier: unavailable. Modified " + i.ModTime().Format(time.RFC3339)
			}
			if e != nil {
				c.Protected = true
				c.ProtectionReason = "Incomplete size scan: " + e.Error()
				errs = append(errs, e)
			}
			cs = append(cs, c)
		}
	}
	for _, item := range []struct{ category, path string }{{"Archives", filepath.Join(base, "Archives")}, {"Simulators", filepath.Join(x.Home, "Library/Developer/CoreSimulator")}, {"System simulators", x.SystemSimulatorRoot}} {
		if item.path == "" {
			continue
		}
		if _, e := os.Lstat(item.path); errors.Is(e, os.ErrNotExist) {
			continue
		} else if e != nil {
			errs = append(errs, e)
			continue
		}
		n, e := storage.TreeBytes(ctx, item.path)
		if e != nil {
			errs = append(errs, e)
		}
		cs = append(cs, storage.Candidate{ID: "xcode:" + item.path, Provider: x.ID(), Category: item.category, Name: item.category, Path: item.path, EstimatedBytes: n, Protected: true, ProtectionReason: "Inspection only. Archives contain release builds and dSYMs; simulator runtimes must be managed by supported Xcode tools."})
	}
	// Read-only tool discovery. Failure does not block directory analysis.
	for _, args := range [][]string{{"simctl", "list", "devices", "unavailable", "--json"}, {"simctl", "list", "runtimes", "--json"}} {
		out, e := x.Run.Run(ctx, "xcrun", args...)
		if e != nil {
			errs = append(errs, e)
			continue
		}
		cs = append(cs, storage.Candidate{ID: "xcode:tool:" + strings.Join(args, "/"), Provider: x.ID(), Category: "Simulator inventory", Name: strings.Join(args[2:], " "), Description: out, Protected: true, ProtectionReason: "Inspection only; no runtime bundles are deleted"})
	}
	return cs, errors.Join(errs...)
}
func (x *Xcode) Plan(ctx context.Context, c storage.Candidate, _ int) (storage.Operation, error) {
	if c.Category != "DerivedData" || !c.Eligible() {
		return storage.Operation{}, errors.New("only eligible DerivedData projects may be cleaned")
	}
	if e := storage.ValidateTarget(ctx, x.derivedRoot(), c.Path); e != nil {
		return storage.Operation{}, e
	}
	return storage.Operation{Candidate: c, Scope: "Permanently delete DerivedData project: " + c.Path, Warning: "Permanent deletion of build products and indexes. Xcode will rebuild them. Xcode and xcodebuild must be closed."}, nil
}
func (x *Xcode) Clean(ctx context.Context, op storage.Operation) (storage.Result, error) {
	c := op.Candidate
	if _, e := x.Plan(ctx, c, 0); e != nil {
		return storage.Result{}, e
	}
	out, e := x.Run.Run(ctx, "/bin/ps", "-axo", "comm=")
	if e != nil {
		return storage.Result{}, fmt.Errorf("cannot check active Xcode processes: %w", e)
	}
	for _, line := range strings.Split(out, "\n") {
		switch filepath.Base(strings.TrimSpace(line)) {
		case "Xcode", "xcodebuild":
			return storage.Result{}, errors.New("Xcode or xcodebuild is active; close it and rescan")
		}
	}
	info, e := os.Lstat(c.Path)
	if e != nil {
		return storage.Result{}, e
	}
	if fileIdentity(info) != c.Metadata["identity"] {
		return storage.Result{}, errors.New("project directory changed since scan; rescan required")
	}
	n, e := storage.TreeBytes(ctx, c.Path)
	if e != nil {
		return storage.Result{}, e
	}
	e = storage.DeleteChild(ctx, x.derivedRoot(), c.Path, info)
	result := storage.Result{ProviderUsageBeforeBytes: &n, Output: "DerivedData removal attempted: " + c.Path + ". Provider does not report reclaimed bytes."}
	if e == nil {
		zero := uint64(0)
		result.ProviderUsageAfterBytes = &zero
	}
	return result, e
}
