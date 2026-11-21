package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

// DeviceSource is deliberately queried again at review and immediately before
// deleting device support. An error never authorizes deletion.
type DeviceSource interface {
	Discover(context.Context) (DeviceInventory, error)
}
type DeviceInventory struct {
	Devices []AppleDevice
	Sources []string
	At      time.Time
}
type AppleDevice struct {
	ID, Name, Platform, Version, Build string
	Available                          bool
}
type XcodeDevices struct{ Run runner.Runner }

func platformName(s string) string {
	switch strings.ToLower(s) {
	case "ios", "com.apple.platform.iphoneos":
		return "iOS"
	case "watchos", "com.apple.platform.watchos":
		return "watchOS"
	case "tvos", "com.apple.platform.appletvos":
		return "tvOS"
	case "visionos", "xros", "com.apple.platform.xros":
		return "visionOS"
	case "macos", "macosx", "com.apple.platform.macosx":
		return "macOS"
	}
	return ""
}

var osVersion = regexp.MustCompile(`^(\d+(?:\.\d+){0,3})(?:\s+\(([A-Za-z0-9]+)\))?$`)
var supportName = regexp.MustCompile(`^(?:((?:iPhone|iPad|iPod|Watch|AppleTV|RealityDevice|Mac)[A-Za-z0-9,_-]*)[ _]+)?(\d+(?:\.\d+){1,3})\s+\(([A-Za-z0-9]+)\)(?:\s+(?:arm64|arm64e))?$`)

func normalizeVersion(s string) string {
	parts := strings.Split(s, ".")
	for i, p := range parts {
		n, e := strconv.Atoi(p)
		if e != nil || n < 0 {
			return ""
		}
		parts[i] = strconv.Itoa(n)
	}
	for len(parts) > 1 && parts[len(parts)-1] == "0" {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, ".")
}
func versionParts(s string) (string, string) {
	m := osVersion.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", ""
	}
	return normalizeVersion(m[1]), m[2]
}
func ParseXCDevices(out string) ([]AppleDevice, error) {
	var rows []struct {
		Identifier, Name, Platform, OperatingSystemVersion string
		Simulator, Available                               *bool
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		return nil, errors.New("xcdevice did not return a JSON array")
	}
	if e := json.Unmarshal([]byte(out), &rows); e != nil {
		return nil, e
	}
	var devices []AppleDevice
	for _, r := range rows {
		if r.Simulator == nil {
			return nil, errors.New("xcdevice simulator field missing")
		}
		if *r.Simulator {
			continue
		}
		if r.Identifier == "" || r.Available == nil {
			return nil, errors.New("xcdevice physical-device identity/availability missing")
		}
		v, b := versionParts(r.OperatingSystemVersion)
		devices = append(devices, AppleDevice{ID: r.Identifier, Name: r.Name, Platform: platformName(r.Platform), Version: v, Build: b, Available: *r.Available})
	}
	return devices, nil
}
func ParseCoreDevices(data []byte) ([]AppleDevice, error) {
	var doc struct {
		Info   struct{ Outcome string }
		Result struct{ Devices json.RawMessage }
	}
	if e := json.Unmarshal(data, &doc); e != nil {
		return nil, e
	}
	if doc.Info.Outcome != "success" || len(doc.Result.Devices) == 0 || doc.Result.Devices[0] != '[' {
		return nil, errors.New("devicectl inventory is incomplete or unsuccessful")
	}
	var rows []struct {
		Identifier           string
		DeviceProperties     *struct{ Name, OsVersionNumber, OsBuildUpdate string }
		HardwareProperties   *struct{ UDID, Platform, Reality string }
		ConnectionProperties *struct{ TunnelState string }
	}
	if e := json.Unmarshal(doc.Result.Devices, &rows); e != nil {
		return nil, e
	}
	var devices []AppleDevice
	for _, r := range rows {
		if r.HardwareProperties != nil && r.HardwareProperties.Reality == "virtual" {
			continue
		}
		if r.Identifier == "" || r.DeviceProperties == nil || r.HardwareProperties == nil || r.ConnectionProperties == nil {
			return nil, errors.New("unsupported devicectl device schema; support folders remain protected")
		}
		id := r.HardwareProperties.UDID
		if id == "" {
			id = r.Identifier
		}
		v, _ := versionParts(r.DeviceProperties.OsVersionNumber)
		devices = append(devices, AppleDevice{ID: id, Name: r.DeviceProperties.Name, Platform: platformName(r.HardwareProperties.Platform), Version: v, Build: r.DeviceProperties.OsBuildUpdate, Available: r.ConnectionProperties.TunnelState == "connected"})
	}
	return devices, nil
}
func (d XcodeDevices) Discover(ctx context.Context) (DeviceInventory, error) {
	ctx = runner.WithoutOutput(ctx)
	inventory := DeviceInventory{At: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, e := d.Run.Run(ctx, "xcrun", "xcdevice", "list", "--timeout", "10")
	if e != nil {
		return inventory, fmt.Errorf("xcdevice inventory: %w", e)
	}
	inventory.Devices, e = ParseXCDevices(out)
	if e != nil {
		return inventory, fmt.Errorf("xcdevice inventory: %w", e)
	}
	inventory.Sources = []string{"xcdevice"}
	// xcdevice covers older devices too. Where CoreDevice is installed, both
	// supported inventories must succeed. Known unavailable/locked devices are
	// included conservatively instead of being treated as disconnected.
	found, findErr := d.Run.Run(ctx, "xcrun", "--find", "devicectl")
	if findErr != nil {
		return inventory, fmt.Errorf("CoreDevice executable unavailable; device-support cleanup requires both supported inventories: %w", findErr)
	}
	if !filepath.IsAbs(strings.TrimSpace(found)) {
		return inventory, errors.New("devicectl executable discovery returned an unexpected path")
	}
	dir, e := os.MkdirTemp("", "devclean-devices-")
	if e != nil {
		return inventory, e
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "devices.json")
	if _, e = d.Run.Run(ctx, "xcrun", "devicectl", "list", "devices", "--timeout", "10", "--json-output", path); e != nil {
		return inventory, fmt.Errorf("CoreDevice inventory: %w", e)
	}
	f, e := os.Open(path)
	if e != nil {
		return inventory, e
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if e != nil {
		return inventory, e
	}
	if len(data) > 2<<20 {
		return inventory, errors.New("CoreDevice inventory exceeds size limit")
	}
	rows, e := ParseCoreDevices(data)
	if e != nil {
		return inventory, e
	}
	inventory.Devices = append(inventory.Devices, rows...)
	inventory.Sources = append(inventory.Sources, "devicectl")
	return inventory, nil
}
func (x *Xcode) deviceInventory(ctx context.Context) (DeviceInventory, error) {
	if x.Devices != nil {
		return x.Devices.Discover(ctx)
	}
	return (XcodeDevices{Run: x.Run}).Discover(ctx)
}
func isDeviceSupport(category string) bool {
	switch category {
	case "iOS DeviceSupport", "watchOS DeviceSupport", "tvOS DeviceSupport", "visionOS DeviceSupport", "macOS DeviceSupport":
		return true
	}
	return false
}
func supportDetails(category, name string, modified time.Time) *storage.DeviceSupportInfo {
	info := &storage.DeviceSupportInfo{Platform: strings.TrimSuffix(category, " DeviceSupport"), Recent: time.Since(modified) < 30*24*time.Hour}
	m := supportName.FindStringSubmatch(name)
	if m == nil {
		return info
	}
	info.DeviceIdentifier = m[1]
	info.Version = m[2]
	info.Build = m[3]
	return info
}
func supportProtection(info *storage.DeviceSupportInfo, inventory DeviceInventory, err error) string {
	if err != nil {
		return "Device inventory unavailable; cleanup blocked: " + err.Error()
	}
	if info.Version == "" || info.Build == "" {
		return "Unrecognized support-folder version/build; cleanup blocked"
	}
	for _, d := range inventory.Devices {
		if d.Platform == "" {
			return "Known device has an unknown platform; cleanup blocked"
		}
		if d.Platform != info.Platform {
			continue
		}
		if d.Version == "" {
			return "Known " + d.Platform + " device has unknown OS version; cleanup blocked"
		}
		if normalizeVersion(d.Version) == normalizeVersion(info.Version) || (d.Build != "" && strings.EqualFold(d.Build, info.Build)) {
			info.MatchedDeviceIDs = append(info.MatchedDeviceIDs, d.ID)
		}
	}
	if len(info.MatchedDeviceIDs) > 0 {
		return "Matches a connected or known physical device; support files are protected"
	}
	return ""
}
