package providers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type Xcode struct {
	Home                string
	Run                 runner.Runner
	SystemSimulatorRoot string
	Devices             DeviceSource
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
	var inventory DeviceInventory
	var inventoryErr error
	inventoryRead := false
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
				if !inventoryRead {
					inventory, inventoryErr = x.deviceInventory(ctx)
					inventoryRead = true
					if inventoryErr != nil {
						errs = append(errs, inventoryErr)
					}
				}
				c.DeviceSupport = supportDetails(category, entry.Name(), i.ModTime())
				c.DeviceSupport.InventorySources = inventory.Sources
				c.DeviceSupport.InventoryAt = inventory.At
				c.Reclaimable = true
				c.ReviewRequired = true
				c.Risk = storage.High
				c.ProtectionReason = supportProtection(c.DeviceSupport, inventory, inventoryErr)
				c.Protected = c.ProtectionReason != ""
				if ve := storage.ValidateTarget(ctx, parent, path); ve != nil {
					c.Protected = true
					c.ProtectionReason = ve.Error()
				}
				c.Description = fmt.Sprintf("Platform: %s · OS: %s · Build: %s · Device identifier: %s · Matched physical device IDs: %s. Individual review required. Historical support files may be needed to symbolize older crash reports. Recent (<30 days): %t. Device inventory: %s.", c.DeviceSupport.Platform, c.DeviceSupport.Version, c.DeviceSupport.Build, c.DeviceSupport.DeviceIdentifier, strings.Join(c.DeviceSupport.MatchedDeviceIDs, ", "), c.DeviceSupport.Recent, strings.Join(inventory.Sources, ", "))

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
func (x *Xcode) cleanupRoot(c storage.Candidate) (string, error) {
	if c.Category == "DerivedData" {
		return x.derivedRoot(), nil
	}
	if isDeviceSupport(c.Category) {
		return filepath.Join(x.Home, "Library/Developer/Xcode", c.Category), nil
	}
	return "", errors.New("unsupported Xcode cleanup category")
}
func (x *Xcode) Plan(ctx context.Context, c storage.Candidate, _ int) (storage.Operation, error) {
	root, e := x.cleanupRoot(c)
	if e != nil {
		return storage.Operation{}, e
	}
	if !c.Eligible() {
		return storage.Operation{}, errors.New("Xcode candidate is protected or has not been individually reviewed")
	}
	if e = storage.ValidateTarget(ctx, root, c.Path); e != nil {
		return storage.Operation{}, e
	}
	if c.Category == "DerivedData" {
		return storage.Operation{Candidate: c, Scope: "Permanently delete DerivedData project: " + c.Path, Warning: "Permanent deletion of build products and indexes. Xcode will rebuild them. Xcode and xcodebuild must be closed."}, nil
	}
	if !c.ReviewRequired || !c.Reviewed {
		return storage.Operation{}, errors.New("device support requires individual review")
	}
	info, e := os.Lstat(c.Path)
	if e != nil {
		return storage.Operation{}, e
	}
	if fileIdentity(info) != c.Metadata["identity"] {
		return storage.Operation{}, errors.New("support folder changed since review; rescan required")
	}
	inventory, e := x.deviceInventory(ctx)
	details := supportDetails(c.Category, filepath.Base(c.Path), info.ModTime())
	if reason := supportProtection(details, inventory, e); reason != "" {
		return storage.Operation{}, errors.New(reason)
	}
	c.DeviceSupport = details
	c.DeviceSupport.InventoryAt = inventory.At
	c.DeviceSupport.InventorySources = inventory.Sources
	return storage.Operation{Candidate: c, Scope: "Permanently delete reviewed " + c.Category + ": " + c.Path, Warning: fmt.Sprintf("Permanent deletion. OS %s, build %s. Historical device support may be required to symbolize older crash reports. Recent (<30 days): %t. A fresh physical-device check passed using %s. Close Xcode and xcodebuild; pins and connected-device protections cannot be overridden.", details.Version, details.Build, details.Recent, strings.Join(inventory.Sources, ", "))}, nil
}
func (x *Xcode) Clean(ctx context.Context, op storage.Operation) (storage.Result, error) {
	c := op.Candidate
	if _, e := x.Plan(ctx, c, 0); e != nil {
		return storage.Result{}, e
	}
	if e := x.ensureXcodeClosed(ctx); e != nil {
		return storage.Result{}, e
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
	root, e := x.cleanupRoot(c)
	if e != nil {
		return storage.Result{}, e
	}
	// Final device and process checks run after the filesystem validation walk.
	e = storage.DeleteChildGuarded(ctx, root, c.Path, info, func(ctx context.Context) error {
		if isDeviceSupport(c.Category) {
			if _, e := x.Plan(ctx, c, 0); e != nil {
				return e
			}
		}
		return x.ensureXcodeClosed(ctx)
	})
	result := storage.Result{ProviderUsageBeforeBytes: &n, Output: "Xcode cleanup attempted: " + c.Path + ". Provider does not report reclaimed bytes."}
	if e == nil {
		zero := uint64(0)
		result.ProviderUsageAfterBytes = &zero
	}
	return result, e
}

func (x *Xcode) ensureXcodeClosed(ctx context.Context) error {
	out, e := x.Run.Run(ctx, "/bin/ps", "-axo", "comm=")
	if e != nil {
		return fmt.Errorf("cannot check active Xcode processes: %w", e)
	}
	for _, line := range strings.Split(out, "\n") {
		switch filepath.Base(strings.TrimSpace(line)) {
		case "Xcode", "xcodebuild":
			return errors.New("Xcode or xcodebuild is active; close it and rescan")
		}
	}

	return nil
}
