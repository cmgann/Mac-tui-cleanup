package providers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type inventorySource struct {
	inventory DeviceInventory
	err       error
	calls     int
	failAt    int
}

func (s *inventorySource) Discover(context.Context) (DeviceInventory, error) {
	s.calls++
	if s.failAt > 0 && s.calls >= s.failAt {
		return s.inventory, errors.New("device service unavailable")
	}
	return s.inventory, s.err
}

const xcInventory = `[
 {"identifier":"connected-test-phone","name":"Fixture phone","platform":"com.apple.platform.iphoneos","operatingSystemVersion":"18.0 (22A100)","simulator":false,"available":true},
 {"identifier":"locked-test-watch","name":"Fixture watch","platform":"com.apple.platform.watchos","operatingSystemVersion":"11.0 (22R100)","simulator":false,"available":false},
 {"simulator":true}
]`
const coreInventory = `{"info":{"outcome":"success","jsonVersion":2},"result":{"devices":[{"identifier":"core-id","hardwareProperties":{"udid":"fixture-udid","platform":"iOS"},"deviceProperties":{"name":"Fixture phone","osVersionNumber":"18.1.0","osBuildUpdate":"22B100"},"connectionProperties":{"tunnelState":"connected"}}]}}`

func TestDeviceParsersFailClosed(t *testing.T) {
	rows, e := ParseXCDevices(xcInventory)
	if e != nil || len(rows) != 2 || rows[0].Version != "18" || rows[1].Available {
		t.Fatal(rows, e)
	}
	core, e := ParseCoreDevices([]byte(coreInventory))
	if e != nil || len(core) != 1 || core[0].Version != "18.1" || core[0].ID != "fixture-udid" {
		t.Fatal(core, e)
	}
	for _, out := range []string{`{}`, `null`, `[{}]`, `[{"simulator":false,"identifier":"id"}]`, `[]garbage`} {
		if _, e := ParseXCDevices(out); e == nil {
			t.Fatalf("accepted incomplete xcdevice output: %s", out)
		}
	}
	for _, out := range []string{`{}`, `{"info":{"outcome":"timeout"},"result":{"devices":[]}}`, `{"info":{"outcome":"success"},"result":{"devices":null}}`, `{"info":{"outcome":"success"},"result":{"devices":[{"identifier":"new-schema","properties":{}}]}}`} {
		if _, e := ParseCoreDevices([]byte(out)); e == nil {
			t.Fatalf("accepted incomplete CoreDevice output: %s", out)
		}
	}
	if rows, e := ParseXCDevices(`[]`); e != nil || len(rows) != 0 {
		t.Fatal(rows, e)
	}
}
func TestDeviceDiscoveryUsesBothSupportedInventories(t *testing.T) {
	f := &fakeRunner{fn: func(name string, args []string) (string, error) {
		s := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(s, "xcdevice list"):
			return xcInventory, nil
		case s == "--find devicectl":
			return "/Applications/Xcode.app/Contents/Developer/usr/bin/devicectl", nil
		case strings.HasPrefix(s, "devicectl list devices"):
			return "Unstructured human output", os.WriteFile(args[len(args)-1], []byte(coreInventory), 0600)
		}
		return "", errors.New("unexpected command")
	}}
	inv, e := (XcodeDevices{Run: f}).Discover(context.Background())
	if e != nil || len(inv.Devices) != 3 || len(inv.Sources) != 2 {
		t.Fatal(inv, e)
	}
	old := f.fn
	f.fn = func(n string, a []string) (string, error) {
		if a[0] == "devicectl" {
			return "", errors.New("timed out")
		}
		return old(n, a)
	}
	if _, e := (XcodeDevices{Run: f}).Discover(context.Background()); e == nil {
		t.Fatal("CoreDevice failure silently ignored")
	}
	f.fn = func(n string, a []string) (string, error) {
		if a[0] == "--find" {
			return "", errors.New("missing utility")
		}
		return old(n, a)
	}
	if _, e := (XcodeDevices{Run: f}).Discover(context.Background()); e == nil {
		t.Fatal("unavailable CoreDevice allowed cleanup")
	}
}
func TestSupportParsingAndMatching(t *testing.T) {
	source, _ := ParseXCDevices(xcInventory)
	inv := DeviceInventory{Devices: source}
	for _, name := range []string{"18.0 (22A100)", "iPhone15,3 18.0 (22A100)", "18.0.0 (22A999)"} {
		info := supportDetails("iOS DeviceSupport", name, time.Now())
		if reason := supportProtection(info, inv, nil); reason == "" || len(info.MatchedDeviceIDs) == 0 {
			t.Fatalf("current version not protected: %s", name)
		}
	}
	info := supportDetails("iOS DeviceSupport", "19.0 (23A100)", time.Now())
	if reason := supportProtection(info, inv, nil); reason != "" {
		t.Fatal("newest folder incorrectly assumed current", reason)
	}
	if !info.Recent {
		t.Fatal("recent entry not identified")
	}
	for _, name := range []string{"18.0", "unknown", "../18.0 (22A100)", "Project 18.0 (22A100)"} {
		if supportProtection(supportDetails("iOS DeviceSupport", name, time.Now()), inv, nil) == "" {
			t.Fatal("unknown directory accepted", name)
		}
	}
	for _, d := range []AppleDevice{{Platform: "iOS"}, {Platform: "", Version: "1"}} {
		if supportProtection(info, DeviceInventory{Devices: []AppleDevice{d}}, nil) == "" {
			t.Fatal("unknown device information authorized cleanup")
		}
	}
}
func supportFixture(t *testing.T) (*Xcode, *inventorySource, string, string) {
	t.Helper()
	home := tempDir(t)
	parent := filepath.Join(home, "Library/Developer/Xcode/iOS DeviceSupport")
	old := filepath.Join(parent, "iPhone14,5 17.0 (21A100)")
	current := filepath.Join(parent, "18.0 (22A100)")
	for _, p := range []string{old, current} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(p, "symbols"), make([]byte, 16384), 0600); e != nil {
			t.Fatal(e)
		}
	}
	rows, _ := ParseXCDevices(xcInventory)
	source := &inventorySource{inventory: DeviceInventory{Devices: rows, Sources: []string{"fixture"}, At: time.Now()}}
	x := &Xcode{Home: home, Devices: source, Run: &fakeRunner{fn: func(string, []string) (string, error) { return "", nil }}}
	return x, source, old, current
}
func supportCandidate(t *testing.T, x *Xcode, path string) storage.Candidate {
	t.Helper()
	cs, e := x.Scan(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range cs {
		if c.Path == path {
			return c
		}
	}
	t.Fatal("support candidate missing")
	return storage.Candidate{}
}
func TestReviewedDeviceSupportDeletion(t *testing.T) {
	x, source, old, current := supportFixture(t)
	c := supportCandidate(t, x, old)
	if c.Eligible() || c.Protected || !c.ReviewRequired || !c.DeviceSupport.Recent {
		t.Fatal("recent support not gated", c)
	}
	if _, e := x.Plan(context.Background(), c, 0); e == nil {
		t.Fatal("unreviewed cleanup accepted")
	}
	c.Reviewed = true
	op, e := x.Plan(context.Background(), c, 0)
	if e != nil || !strings.Contains(op.Warning, "symbolize") {
		t.Fatal(op, e)
	}
	result, e := x.Clean(context.Background(), op)
	if e != nil || result.ProviderReportedBytes != nil || result.ProviderUsageAfterBytes == nil || *result.ProviderUsageAfterBytes != 0 {
		t.Fatal(result, e)
	}
	if _, e := os.Stat(old); !os.IsNotExist(e) {
		t.Fatal("reviewed folder not removed")
	}
	if _, e := os.Stat(current); e != nil {
		t.Fatal("current support was changed", e)
	}
	if source.calls < 4 {
		t.Fatal("missing scan/review/execution/device-boundary revalidation", source.calls)
	}
}
func TestConnectedDeviceChangesAndFailuresBlockDeletion(t *testing.T) {
	for _, mode := range []string{"new-device", "service-error", "last-boundary-error", "pinned", "changed-directory"} {
		t.Run(mode, func(t *testing.T) {
			x, source, old, _ := supportFixture(t)
			c := supportCandidate(t, x, old)
			c.Reviewed = true
			op, e := x.Plan(context.Background(), c, 0)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "new-device":
				source.inventory.Devices = append(source.inventory.Devices, AppleDevice{ID: "newly-connected", Platform: "iOS", Version: "17", Build: "21A100"})
			case "service-error":
				source.err = errors.New("service unavailable")
			case "last-boundary-error":
				source.failAt = source.calls + 2
			case "pinned":
				op.Candidate.Protected = true
			case "changed-directory":
				os.Chtimes(old, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
			}
			if _, e := x.Clean(context.Background(), op); e == nil {
				t.Fatal("unsafe deletion accepted")
			}
			if _, e := os.Stat(filepath.Join(old, "symbols")); e != nil {
				t.Fatal("protected files modified", e)
			}
		})
	}
}
func TestDeviceDiscoveryFailureProtectsScan(t *testing.T) {
	x, source, old, _ := supportFixture(t)
	source.err = errors.New("device service offline")
	cs, e := x.Scan(context.Background())
	if e == nil {
		t.Fatal("failed inventory not reported")
	}
	for _, c := range cs {
		if c.Path == old && (!c.Protected || c.Eligible()) {
			t.Fatal("failed discovery authorized selection")
		}
	}
}
