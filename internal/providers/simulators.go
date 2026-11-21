package providers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

const simulatorDevices = "Simulator devices"
const simulatorRuntimes = "Simulator runtimes"

// DeviceSet is injected only for disposable integration tests. The application
// always uses the current user's standard device set, passed explicitly to simctl.
type Simulators struct {
	Home      string
	Run       runner.Runner
	DeviceSet string
}

func (*Simulators) ID() string { return "Xcode" }
func (s *Simulators) set() string {
	if s.DeviceSet != "" {
		return s.DeviceSet
	}
	return filepath.Join(s.Home, "Library/Developer/CoreSimulator/Devices")
}

var simulatorUUID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

type SimDevice struct {
	UDID, Name, State, DataPath, LogPath, LastBootedAt string
	IsAvailable                                        *bool
	Runtime                                            string `json:"-"`
}
type SimProfile struct {
	Identifier, Name, Version, Buildversion, BundlePath string
	IsAvailable                                         *bool
}
type SimImage struct {
	Identifier, Build, Version, Kind, State, RuntimeIdentifier, RuntimeBundlePath, LastUsedAt string
	Deletable                                                                                 *bool
	SizeBytes                                                                                 json.RawMessage
}

func ParseSimDevices(out string) ([]SimDevice, error) {
	var doc struct{ Devices map[string][]SimDevice }
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		return nil, e
	}
	if doc.Devices == nil {
		return nil, errors.New("simctl device inventory missing")
	}
	var rows []SimDevice
	seen := map[string]bool{}
	for runtime, devices := range doc.Devices {
		if !strings.HasPrefix(runtime, "com.apple.CoreSimulator.SimRuntime.") {
			return nil, errors.New("unknown simulator runtime identifier")
		}
		for _, d := range devices {
			if !simulatorUUID.MatchString(d.UDID) || d.Name == "" || d.State == "" || d.IsAvailable == nil || seen[d.UDID] {
				return nil, errors.New("incomplete or duplicate simulator device record")
			}
			seen[d.UDID] = true
			d.Runtime = runtime
			rows = append(rows, d)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].UDID < rows[j].UDID })
	return rows, nil
}
func ParseSimProfiles(out string) ([]SimProfile, error) {
	var doc struct{ Runtimes []SimProfile }
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		return nil, e
	}
	if doc.Runtimes == nil {
		return nil, errors.New("simctl runtime profiles missing")
	}
	for _, r := range doc.Runtimes {
		if !strings.HasPrefix(r.Identifier, "com.apple.CoreSimulator.SimRuntime.") {
			return nil, errors.New("unknown simulator runtime profile")
		}
	}
	return doc.Runtimes, nil
}
func ParseSimImages(out string) ([]SimImage, error) {
	var doc map[string]SimImage
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		return nil, e
	}
	if doc == nil {
		return nil, errors.New("simctl managed runtime inventory missing")
	}
	var rows []SimImage
	for id, r := range doc {
		if r.Identifier == "" {
			r.Identifier = id
		}
		if !simulatorUUID.MatchString(id) || r.Identifier != id {
			return nil, errors.New("unknown managed runtime identity")
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Identifier < rows[j].Identifier })
	return rows, nil
}
func fingerprint(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func (s *Simulators) tool(ctx context.Context) (string, error) {
	out, e := s.Run.Run(runner.WithoutOutput(ctx), "xcrun", "--find", "simctl")
	if e != nil {
		return "", e
	}
	tool := strings.TrimSpace(out)
	if !filepath.IsAbs(tool) || filepath.Clean(tool) != tool || filepath.Base(tool) != "simctl" || strings.ContainsAny(tool, "\r\n") {
		return "", errors.New("invalid simctl executable discovery")
	}
	return tool, nil
}
func (s *Simulators) devices(ctx context.Context, tool string) ([]SimDevice, error) {
	out, e := s.Run.Run(runner.WithoutOutput(ctx), tool, "--set", s.set(), "list", "devices", "--json")
	if e != nil {
		return nil, e
	}
	return ParseSimDevices(out)
}
func (s *Simulators) profiles(ctx context.Context, tool string) ([]SimProfile, error) {
	out, e := s.Run.Run(runner.WithoutOutput(ctx), tool, "--set", s.set(), "list", "runtimes", "--json")
	if e != nil {
		return nil, e
	}
	return ParseSimProfiles(out)
}
func (s *Simulators) images(ctx context.Context, tool string) ([]SimImage, error) {
	out, e := s.Run.Run(runner.WithoutOutput(ctx), tool, "--set", s.set(), "runtime", "list", "--json")
	if e != nil {
		return nil, e
	}
	return ParseSimImages(out)
}
func (s *Simulators) devicePath(ctx context.Context, d SimDevice) (string, error) {
	root := s.set()
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return "", errors.New("invalid simulator device-set root")
	}
	path := filepath.Join(root, d.UDID)
	if d.DataPath != "" && d.DataPath != filepath.Join(path, "data") {
		return path, errors.New("simulator data path is outside its approved device directory")
	}
	if d.LogPath != "" && d.LogPath != filepath.Join(s.Home, "Library/Logs/CoreSimulator", d.UDID) {
		return path, errors.New("simulator log path is outside the approved log directory")
	}
	if d.LogPath != "" {
		parent := filepath.Dir(d.LogPath)
		real, e := filepath.EvalSymlinks(parent)
		if e == nil && real != parent {
			return path, errors.New("symlinked simulator log root protected")
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return path, e
		}
		if info, e := os.Lstat(d.LogPath); e == nil {
			p, e := os.Stat(parent)
			if e != nil {
				return path, e
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Sys().(*syscall.Stat_t).Dev != p.Sys().(*syscall.Stat_t).Dev {
				return path, errors.New("unexpected simulator log symlink or mount")
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return path, e
		}
	}
	real, e := filepath.EvalSymlinks(root)
	if e != nil {
		return path, e
	}
	if real != root {
		return path, errors.New("symlinked simulator device set protected")
	}
	info, e := os.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		return path, nil
	}
	if e != nil {
		return path, e
	}
	parent, e := os.Stat(root)
	if e != nil {
		return path, e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Sys().(*syscall.Stat_t).Dev != parent.Sys().(*syscall.Stat_t).Dev {
		return path, errors.New("unexpected simulator symlink, special entry or mount")
	}
	return path, ctx.Err()
}
func (s *Simulators) deviceCandidate(ctx context.Context, tool string, d SimDevice) storage.Candidate {
	c := storage.Candidate{ID: "simulator:device:" + d.UDID, Provider: s.ID(), Category: simulatorDevices, Name: d.Name, Reclaimable: true, ReviewRequired: true, Risk: storage.High, Metadata: map[string]string{"kind": "device", "resource": d.UDID, "runtime": d.Runtime, "tool": tool, "set": s.set(), "identity": fingerprint(struct {
		Device  SimDevice
		Runtime string
	}{d, d.Runtime})}}
	c.Path = filepath.Join(s.set(), d.UDID)
	c.Description = fmt.Sprintf("Device %s · Runtime %s · State %s · Available %t. Deleting this simulator permanently loses its installed apps, test databases, settings and logs. Individual review required.", d.UDID, d.Runtime, d.State, *d.IsAvailable)
	if d.State != "Shutdown" {
		c.Protected = true
		c.ProtectionReason = "Only shutdown simulators may be deleted; booted and transitional devices are protected"
	}
	path, e := s.devicePath(ctx, d)
	if e != nil {
		c.Protected = true
		c.ProtectionReason = e.Error()
	} else {
		n, e := storage.TreeBytes(ctx, path)
		if errors.Is(e, os.ErrNotExist) {
			c.Description += " Data directory is absent; unique reclaimable size is unknown."
		} else if e != nil {
			c.Protected = true
			c.ProtectionReason = "Incomplete simulator measurement: " + e.Error()
		} else {
			c.EstimatedBytes = n
		}
		if info, e := os.Stat(path); e == nil {
			c.LastModified = info.ModTime()
		}
	}
	return c
}
func runtimeProfile(image SimImage, profiles []SimProfile) string {
	if image.RuntimeIdentifier != "" {
		for _, r := range profiles {
			if r.Identifier == image.RuntimeIdentifier {
				return r.Identifier
			}
		}
		return ""
	}
	var match string
	for _, r := range profiles {
		if image.RuntimeBundlePath != "" && r.BundlePath == image.RuntimeBundlePath {
			if match != "" {
				return ""
			}
			match = r.Identifier
		}
	}
	return match
}
func (s *Simulators) runtimeCandidate(tool string, r SimImage, profiles []SimProfile, devices []SimDevice) storage.Candidate {
	c := storage.Candidate{ID: "simulator:runtime:" + r.Identifier, Provider: s.ID(), Category: simulatorRuntimes, Name: "Runtime " + r.Version + " (" + r.Build + ")", Reclaimable: true, ReviewRequired: true, Risk: storage.High, Metadata: map[string]string{"kind": "runtime", "resource": r.Identifier, "tool": tool, "set": s.set(), "identity": fingerprint(struct{ ID, Build, Version, Kind, Runtime, Bundle string }{r.Identifier, r.Build, r.Version, r.Kind, r.RuntimeIdentifier, r.RuntimeBundlePath})}}
	profile := runtimeProfile(r, profiles)
	c.Metadata["runtime"] = profile
	c.Description = "Managed runtime " + r.Identifier + " · " + r.Kind + " · State " + r.State + ". Removing it requires downloading the runtime again before compatible tests can run. Runtime bundles are never deleted directly. Individual review required."
	if s.DeviceSet != "" {
		c.Protected = true
		c.ProtectionReason = "Runtime deletion requires the standard device set to check references"
	} else if r.Deletable == nil || !*r.Deletable || r.Kind != "Disk Image" || (r.State != "Ready" && r.State != "Unusable") {
		c.Protected = true
		c.ProtectionReason = "Tool does not explicitly identify this managed disk image as deletable"
	} else if profile == "" {
		c.Protected = true
		c.ProtectionReason = "Cannot unambiguously map this image to an installed runtime"
	}
	for _, d := range devices {
		if d.Runtime == profile {
			c.Protected = true
			c.ProtectionReason = "Runtime is referenced by simulator " + d.Name + " (" + d.UDID + ")"
		}
	}
	if len(r.SizeBytes) > 0 {
		var str string
		raw := string(r.SizeBytes)
		if json.Unmarshal(r.SizeBytes, &str) == nil {
			raw = str
		}
		if n, e := ParseSize(raw); e == nil {
			c.EstimatedBytes = n
		} else {
			c.Description += " Size is unreported or unsupported."
		}
	} else {
		c.Description += " Size is unreported."
	}
	c.LastModified, _ = time.Parse(time.RFC3339Nano, r.LastUsedAt)
	return c
}
func (s *Simulators) Scan(ctx context.Context) ([]storage.Candidate, error) {
	tool, e := s.tool(ctx)
	if e != nil {
		return nil, e
	}
	devices, e := s.devices(ctx, tool)
	if e != nil {
		return nil, e
	}
	var cs []storage.Candidate
	for _, d := range devices {
		cs = append(cs, s.deviceCandidate(ctx, tool, d))
	}
	profiles, pe := s.profiles(ctx, tool)
	images, ie := s.images(ctx, tool)
	if pe == nil && ie == nil {
		for _, r := range images {
			cs = append(cs, s.runtimeCandidate(tool, r, profiles, devices))
		}
	} else if pe == nil {
		for _, r := range profiles {
			cs = append(cs, storage.Candidate{ID: "simulator:profile:" + r.Identifier, Provider: s.ID(), Category: simulatorRuntimes, Name: r.Name, Description: "Runtime " + r.Version + " (" + r.Buildversion + ")", Protected: true, ProtectionReason: "Managed runtime deletion inventory is unavailable"})
		}
	}
	return cs, errors.Join(pe, ie)
}
func (s *Simulators) Plan(ctx context.Context, c storage.Candidate, _ int) (storage.Operation, error) {
	if !c.Eligible() || !c.ReviewRequired || !c.Reviewed {
		return storage.Operation{}, errors.New("simulator requires individual review and must remain unprotected")
	}
	tool, e := s.tool(ctx)
	if e != nil {
		return storage.Operation{}, e
	}
	if tool != c.Metadata["tool"] || s.set() != c.Metadata["set"] || !simulatorUUID.MatchString(c.Metadata["resource"]) {
		return storage.Operation{}, errors.New("simulator toolchain, set or identifier changed; rescan required")
	}
	devices, e := s.devices(ctx, tool)
	if e != nil {
		return storage.Operation{}, e
	}
	var fresh storage.Candidate
	found := false
	switch c.Category {
	case simulatorDevices:
		for _, d := range devices {
			if d.UDID == c.Metadata["resource"] {
				fresh = s.deviceCandidate(ctx, tool, d)
				found = true
			}
		}
	case simulatorRuntimes:
		profiles, e := s.profiles(ctx, tool)
		if e != nil {
			return storage.Operation{}, e
		}
		images, e := s.images(ctx, tool)
		if e != nil {
			return storage.Operation{}, e
		}
		for _, r := range images {
			if r.Identifier == c.Metadata["resource"] {
				fresh = s.runtimeCandidate(tool, r, profiles, devices)
				found = true
			}
		}
	default:
		return storage.Operation{}, errors.New("unknown simulator action")
	}
	if !found {
		return storage.Operation{}, errors.New("simulator resource disappeared")
	}
	if fresh.Protected {
		return storage.Operation{}, errors.New(fresh.ProtectionReason)
	}
	if fresh.Metadata["identity"] != c.Metadata["identity"] {
		return storage.Operation{}, errors.New("simulator resource changed since review; rescan required")
	}
	op := storage.Operation{Candidate: c, Warning: fresh.Description}
	if c.Category == simulatorDevices {
		help, e := runner.Combined(ctx, s.Run, tool, "help", "delete")
		if e != nil || !strings.Contains(help, "delete") || !strings.Contains(help, "<device") {
			return storage.Operation{}, fmt.Errorf("installed simctl cannot confirm individual device deletion: %v", e)
		}
		op.Args = []string{"--set", s.set(), "delete", c.Metadata["resource"]}
	} else {
		help, e := runner.Combined(ctx, s.Run, tool, "help", "runtime")
		if e != nil || !strings.Contains(help, "delete") || !strings.Contains(help, "--dry-run") {
			return storage.Operation{}, fmt.Errorf("installed simctl runtime deletion/dry-run not supported: %v", e)
		}
		dry, e := runner.Combined(ctx, s.Run, tool, "--set", s.set(), "runtime", "delete", c.Metadata["resource"], "--dry-run")
		if e != nil {
			return storage.Operation{}, e
		}
		op.Warning += "\nDry run:\n" + dry
		op.Args = []string{"--set", s.set(), "runtime", "delete", c.Metadata["resource"]}
	}
	op.Scope = tool + " " + strings.Join(op.Args, " ")
	return op, nil
}
func (s *Simulators) Clean(ctx context.Context, op storage.Operation) (storage.Result, error) {
	expected, e := s.Plan(ctx, op.Candidate, 0)
	if e != nil {
		return storage.Result{}, e
	}
	if strings.Join(expected.Args, "\x00") != strings.Join(op.Args, "\x00") {
		return storage.Result{}, errors.New("simulator operation scope changed")
	}
	if e = (&Xcode{Run: s.Run}).ensureXcodeClosed(ctx); e != nil {
		return storage.Result{}, e
	}
	// Capability checks and tree measurements can take time. Recheck resource
	// state once more at the command boundary without another expensive walk.
	if e = s.guard(ctx, op.Candidate); e != nil {
		return storage.Result{}, e
	}
	out, e := s.Run.Run(ctx, op.Candidate.Metadata["tool"], op.Args...)
	result := storage.Result{Output: out}
	if e == nil {
		var present bool
		var checkErr error
		if op.Candidate.Category == simulatorDevices {
			var rows []SimDevice
			rows, checkErr = s.devices(ctx, op.Candidate.Metadata["tool"])
			for _, r := range rows {
				present = present || r.UDID == op.Candidate.Metadata["resource"]
			}
		} else {
			var rows []SimImage
			rows, checkErr = s.images(ctx, op.Candidate.Metadata["tool"])
			for _, r := range rows {
				present = present || r.Identifier == op.Candidate.Metadata["resource"]
			}
		}
		if checkErr != nil {
			e = fmt.Errorf("command completed but deletion verification failed: %w", checkErr)
		} else if present {
			e = errors.New("simctl completed but selected resource is still present")
		}
	}
	return result, e
}

func (s *Simulators) guard(ctx context.Context, c storage.Candidate) error {
	tool, e := s.tool(ctx)
	if e != nil {
		return e
	}
	if tool != c.Metadata["tool"] {
		return errors.New("simulator toolchain changed before deletion")
	}
	if c.Category == simulatorDevices {
		devices, e := s.devices(ctx, tool)
		if e != nil {
			return e
		}
		for _, d := range devices {
			if d.UDID != c.Metadata["resource"] {
				continue
			}
			if d.State != "Shutdown" {
				return errors.New("simulator became active before deletion")
			}
			if fingerprint(struct {
				Device  SimDevice
				Runtime string
			}{d, d.Runtime}) != c.Metadata["identity"] {
				return errors.New("simulator changed before deletion")
			}
			_, e = s.devicePath(ctx, d)
			return e
		}
		return errors.New("simulator disappeared before deletion")
	}
	profiles, e := s.profiles(ctx, tool)
	if e != nil {
		return e
	}
	images, e := s.images(ctx, tool)
	if e != nil {
		return e
	}
	devices, e := s.devices(ctx, tool)
	if e != nil {
		return e
	}
	for _, r := range images {
		if r.Identifier == c.Metadata["resource"] {
			fresh := s.runtimeCandidate(tool, r, profiles, devices)
			if fresh.Protected {
				return errors.New(fresh.ProtectionReason)
			}
			if fresh.Metadata["identity"] != c.Metadata["identity"] {
				return errors.New("runtime changed before deletion")
			}
			return ctx.Err()
		}
	}
	return errors.New("runtime disappeared before deletion")
}
