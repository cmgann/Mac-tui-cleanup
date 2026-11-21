package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type Docker struct{ Run runner.Runner }

func (*Docker) ID() string { return "Docker" }

type Builder struct {
	Name, Driver, Err string
	LastActivity      string
	Nodes             []struct{ Name, Endpoint, Status, Err string }
}
type CacheEntry struct {
	ID                  string
	Size                json.RawMessage
	Reclaimable, Shared bool
	LastUsedAt          string
}

func DecodeObjects[T any](s string) ([]T, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	var out []T
	for {
		var raw json.RawMessage
		if e := dec.Decode(&raw); e == io.EOF {
			return out, nil
		} else if e != nil {
			return out, e
		}
		if len(raw) > 0 && raw[0] == '[' {
			var rows []T
			if e := json.Unmarshal(raw, &rows); e != nil {
				return out, e
			}
			out = append(out, rows...)
		} else {
			var v T
			if e := json.Unmarshal(raw, &v); e != nil {
				return out, e
			}
			out = append(out, v)
		}
	}
}
func CacheBytes(rows []CacheEntry, days int) (uint64, uint64, error) {
	var total, reclaim uint64
	seen := map[string]bool{}
	for _, v := range rows {
		var s string
		if len(v.Size) > 0 && v.Size[0] == '"' {
			if e := json.Unmarshal(v.Size, &s); e != nil {
				return 0, 0, e
			}
		} else {
			s = string(v.Size)
		}
		n, e := strconv.ParseUint(s, 10, 64)
		if e != nil {
			n, e = ParseSize(s)
		} // recent Buildx JSON renders human-size strings
		if e != nil {
			return 0, 0, fmt.Errorf("invalid cache size %q", s)
		}
		if seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		total += n
		old := true
		if days > 0 {
			t, e := time.Parse(time.RFC3339Nano, v.LastUsedAt)
			old = e == nil && time.Since(t) >= time.Duration(days)*24*time.Hour
		}
		if v.Reclaimable && !v.Shared && old {
			reclaim += n
		}
	}
	return total, reclaim, nil
}
func (d *Docker) cli(ctx context.Context, args ...string) (string, error) {
	return d.Run.Run(ctx, "docker", args...)
}
func (d *Docker) endpoint(ctx context.Context, name string) (string, error) {
	if strings.HasPrefix(name, "unix://") {
		return name, nil
	}
	out, e := d.cli(ctx, "context", "inspect", name, "--format", "{{json .Endpoints.docker.Host}}")
	if e != nil {
		return "", e
	}
	var host string
	e = json.Unmarshal([]byte(out), &host)
	return host, e
}
func (d *Docker) identity(ctx context.Context) (string, string, error) {
	if os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" || os.Getenv("BUILDX_BUILDER") != "" {
		return "", "", errors.New("Docker environment overrides are set; unset DOCKER_HOST, DOCKER_CONTEXT and BUILDX_BUILDER for a verifiable local scan")
	}
	s, e := d.cli(ctx, "context", "show")
	if e != nil {
		return "", "", e
	}
	name := strings.TrimSpace(s)
	host, e := d.endpoint(ctx, name)
	if e != nil {
		return "", "", e
	}
	if !strings.HasPrefix(host, "unix://") {
		return name, host, errors.New("remote Docker context protected; remote cleanup is unavailable in this release")
	}
	return name, host, nil
}
func (d *Docker) builders(ctx context.Context) ([]Builder, error) {
	out, e := d.cli(ctx, "buildx", "ls", "--format", "{{json .Builder}}")
	if e != nil {
		return nil, e
	}
	return DecodeObjects[Builder](out)
}
func (d *Docker) builderIdentity(ctx context.Context, b Builder) (string, error) {
	if b.Driver != "docker" && b.Driver != "docker-container" {
		return "", errors.New("remote or unsupported builder driver protected")
	}
	if b.Err != "" {
		return "", errors.New(b.Err)
	}
	if len(b.Nodes) != 1 {
		return "", errors.New("multi-node builders protected to avoid overlapping cache accounting")
	}
	n := b.Nodes[0]
	host, e := d.endpoint(ctx, n.Endpoint)
	if e != nil {
		return "", e
	}
	if !strings.HasPrefix(host, "unix://") {
		return "", errors.New("remote builder protected")
	}
	if n.Status != "running" || n.Err != "" {
		return "", fmt.Errorf("builder status %s: %s", n.Status, n.Err)
	}
	key := host + "/" + b.Driver
	if b.Driver == "docker-container" {
		key += "/" + n.Name
	}
	return key, nil
}
func (d *Docker) Scan(ctx context.Context) ([]storage.Candidate, error) {
	name, host, e := d.identity(ctx)
	if e != nil {
		return nil, e
	}
	var cs []storage.Candidate
	var errs []error
	summary, se := d.cli(ctx, "--context", name, "system", "df", "--format", "json")
	if se != nil {
		errs = append(errs, se)
	} else {
		cs = append(cs, storage.Candidate{ID: "docker:overview", Provider: d.ID(), Category: "Overview", Name: "Docker disk usage · " + name, Description: summary, Protected: true, ProtectionReason: "Summary only; figures overlap individual resources"})
	}
	bs, be := d.builders(ctx)
	if be != nil {
		errs = append(errs, be)
	}
	seen := map[string]bool{}
	stores := map[string]bool{}
	for _, b := range bs {
		if seen[b.Name] {
			continue
		}
		seen[b.Name] = true
		c := storage.Candidate{ID: "docker:builder:" + b.Name, Provider: d.ID(), Category: "BuildKit", Name: b.Name, Risk: storage.Medium, Metadata: map[string]string{"context": name, "host": host, "builder": b.Name, "driver": b.Driver}}
		c.LastModified = parseDockerDate(b.LastActivity)
		c.Metadata["age_basis"] = "builder last activity"
		key, err := d.builderIdentity(ctx, b)
		if err != nil {
			c.Protected = true
			c.ProtectionReason = err.Error()
		} else if stores[key] {
			c.Protected = true
			c.ProtectionReason = "Alias of an already counted cache store"
		} else {
			stores[key] = true
			c.Metadata["store"] = key
			c.Metadata["accounting"] = key
			out, err := d.cli(ctx, "--context", name, "buildx", "du", "--builder", b.Name, "--format=json")
			if err != nil {
				c.Protected = true
				c.ProtectionReason = err.Error()
				errs = append(errs, err)
			} else {
				rows, err := DecodeObjects[CacheEntry](out)
				if err == nil {
					var total uint64
					total, c.EstimatedBytes, err = CacheBytes(rows, 0)
					c.Description = fmt.Sprintf("Driver: %s · Status: running · Total cache: %s. Private reclaimable: %s. Shared records excluded. Age policy may recover less.", b.Driver, storage.Bytes(total), storage.Bytes(c.EstimatedBytes))
				}
				if err != nil {
					c.Protected = true
					c.ProtectionReason = err.Error()
					errs = append(errs, err)
				} else {
					c.Reclaimable = true
				}
			}
		}
		cs = append(cs, c)
	}
	if se == nil {
		out, err := d.cli(ctx, "--context", name, "ps", "-a", "--no-trunc", "--size", "--format", "{{json .}}")
		if err != nil {
			errs = append(errs, err)
		} else {
			rows, err := DecodeObjects[map[string]string](out)
			if err != nil {
				errs = append(errs, err)
			}
			for _, v := range rows {
				stopped := v["State"] == "exited" || v["State"] == "created"
				c := storage.Candidate{ID: "docker:container:" + v["ID"], Provider: d.ID(), Category: "Containers", Name: v["Names"], Description: "State: " + v["State"] + " · Created: " + v["CreatedAt"] + " · Writable layer: " + v["Size"] + ". Container-local data will be lost; mounted volumes are retained.", Risk: storage.High, Reclaimable: stopped, Protected: !stopped, ProtectionReason: "Active container", LastModified: parseDockerDate(v["CreatedAt"]), Metadata: map[string]string{"context": name, "host": host, "container": v["ID"], "age_basis": "created"}}
				cs = append(cs, c)
			}
		}
		out, err = d.cli(ctx, "--context", name, "image", "ls", "--no-trunc", "--format", "{{json .}}")
		if err != nil {
			errs = append(errs, err)
		} else {
			rows, err := DecodeObjects[map[string]string](out)
			if err != nil {
				errs = append(errs, err)
			}
			images := map[string]bool{}
			for _, v := range rows {
				if images[v["ID"]] {
					continue
				}
				images[v["ID"]] = true
				refs, err := d.cli(ctx, "--context", name, "ps", "-a", "--filter", "ancestor="+v["ID"], "--format", "{{.ID}}")
				unused := err == nil && strings.TrimSpace(refs) == ""
				if err != nil {
					errs = append(errs, err)
				}
				cs = append(cs, storage.Candidate{ID: "docker:image:" + v["ID"], Provider: d.ID(), Category: "Images", Name: v["Repository"] + ":" + v["Tag"], Description: "ID: " + v["ID"] + " · Nominal size: " + v["Size"] + ". Shared layers excluded from estimate. Removal requires re-download or rebuild.", Risk: storage.High, Reclaimable: unused, Protected: !unused, ProtectionReason: "Referenced by containers or reference check failed", LastModified: parseDockerDate(v["CreatedAt"]), Metadata: map[string]string{"context": name, "host": host, "image": v["ID"], "age_basis": "created"}})
			}
		}
		out, err = d.cli(ctx, "--context", name, "volume", "ls", "--format", "{{json .}}")
		if err != nil {
			errs = append(errs, err)
		} else {
			rows, err := DecodeObjects[map[string]string](out)
			if err != nil {
				errs = append(errs, err)
			}
			for _, v := range rows {
				cs = append(cs, storage.Candidate{ID: "docker:volume:" + v["Name"], Provider: d.ID(), Category: "Volumes", Name: v["Name"], Description: "Persistent application data. Size/reference details may appear in Docker overview. Inspect with docker volume inspect.", Protected: true, ProtectionReason: "All volumes protected; may contain PostgreSQL, MySQL, Redis, Keycloak or other databases"})
			}
		}
	}
	for i := range cs {
		if !cs[i].Protected {
			cs[i].ProtectionReason = ""
		}
	}
	return cs, errors.Join(errs...)
}
func (d *Docker) Plan(ctx context.Context, c storage.Candidate, days int) (storage.Operation, error) {
	if !c.Eligible() {
		return storage.Operation{}, errors.New("Docker candidate protected")
	}
	if days != 0 && days != 7 && days != 30 {
		return storage.Operation{}, errors.New("unsupported age policy")
	}
	op := storage.Operation{Candidate: c, AgeDays: days, Warning: "Permanent removal. Docker disk images may not immediately return space to macOS."}
	m := c.Metadata
	op.Args = []string{"--context", m["context"]}
	switch c.Category {
	case "BuildKit":
		op.Args = append(op.Args, "buildx", "prune", "--builder", m["builder"], "--all", "--force")
		if days > 0 {
			op.Args = append(op.Args, "--filter", fmt.Sprintf("until=%dh", days*24))
		}
		op.Warning += " Build caches will need rebuilding."
	case "Images":
		op.Args = append(op.Args, "image", "rm", m["image"])
		op.Warning += " Image must be downloaded or rebuilt."
	case "Containers":
		op.Args = append(op.Args, "container", "rm", m["container"])
		op.Warning += " Writable-layer data is permanently lost; volumes retained."
	default:
		return op, errors.New("unsupported Docker action")
	}
	op.Scope = "docker " + strings.Join(op.Args, " ")
	return op, nil
}
func (d *Docker) Clean(ctx context.Context, op storage.Operation) (storage.Result, error) {
	c := op.Candidate
	m := c.Metadata
	name, host, e := d.identity(ctx)
	if e != nil {
		return storage.Result{}, e
	}
	if name != m["context"] || host != m["host"] {
		return storage.Result{}, errors.New("Docker context changed; rescan required")
	}
	expected, e := d.Plan(ctx, c, op.AgeDays)
	if e != nil {
		return storage.Result{}, e
	}
	if strings.Join(expected.Args, "\x00") != strings.Join(op.Args, "\x00") {
		return storage.Result{}, errors.New("operation scope changed")
	}
	switch c.Category {
	case "BuildKit":
		bs, e := d.builders(ctx)
		if e != nil {
			return storage.Result{}, e
		}
		found := false
		for _, b := range bs {
			if b.Name == m["builder"] {
				key, e := d.builderIdentity(ctx, b)
				if e != nil {
					return storage.Result{}, e
				}
				if key != m["store"] {
					return storage.Result{}, errors.New("builder endpoint changed")
				}
				found = true
			}
		}
		if !found {
			return storage.Result{}, errors.New("builder disappeared")
		}
		help, e := d.cli(ctx, "buildx", "prune", "--help")
		if e != nil {
			return storage.Result{}, e
		}
		for _, flag := range []string{"--builder", "--all", "--force", "--filter"} {
			if !strings.Contains(help, flag) {
				return storage.Result{}, fmt.Errorf("installed Buildx does not support %s", flag)
			}
		}
	case "Images":
		refs, e := d.cli(ctx, "--context", name, "ps", "-a", "--filter", "ancestor="+m["image"], "--format", "{{.ID}}")
		if e != nil {
			return storage.Result{}, e
		}
		if strings.TrimSpace(refs) != "" {
			return storage.Result{}, errors.New("image is now referenced")
		}
	case "Containers":
		out, e := d.cli(ctx, "--context", name, "container", "inspect", "--format", "{{.State.Status}}", m["container"])
		if e != nil {
			return storage.Result{}, e
		}
		status := strings.TrimSpace(out)
		if status != "exited" && status != "created" {
			return storage.Result{}, errors.New("container is now active")
		}
	default:
		return storage.Result{}, errors.New("unsupported action")
	}
	var before *uint64
	if c.Category == "BuildKit" {
		n, err := d.cacheUsage(ctx, name, m["builder"])
		if err != nil {
			return storage.Result{}, fmt.Errorf("pre-cleanup cache measurement failed: %w", err)
		}
		before = &n
	}
	out, e := d.cli(ctx, op.Args...)
	r := storage.Result{Output: out, ProviderUsageBeforeBytes: before}
	if c.Category == "BuildKit" && ctx.Err() == nil {
		n, err := d.cacheUsage(ctx, name, m["builder"])
		if err != nil {
			r.MeasurementErrors = append(r.MeasurementErrors, "Post-cleanup cache measurement: "+err.Error())
		} else {
			r.ProviderUsageAfterBytes = &n
		}
	}
	var reportErr error
	r.ProviderReportedBytes, reportErr = parsePruneReported(out, c.Category == "BuildKit")
	if reportErr != nil {
		r.MeasurementErrors = append(r.MeasurementErrors, reportErr.Error())
	}
	return r, e
}
func ParseSize(s string) (uint64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	i := 0
	for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0, errors.New("missing size")
	}
	v, e := strconv.ParseFloat(s[:i], 64)
	if e != nil {
		return 0, e
	}
	units := map[string]float64{"": 1, "B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "KiB": 1024, "MiB": 1048576, "GiB": 1073741824}
	mul, ok := units[s[i:]]
	if !ok || v < 0 {
		return 0, errors.New("unknown size unit")
	}
	return uint64(v * mul), nil
}

func (d *Docker) cacheUsage(ctx context.Context, name, builder string) (uint64, error) {
	out, e := d.cli(ctx, "--context", name, "buildx", "du", "--builder", builder, "--format=json")
	if e != nil {
		return 0, e
	}
	rows, e := DecodeObjects[CacheEntry](out)
	if e != nil {
		return 0, e
	}
	n, _, e := CacheBytes(rows, 0)
	return n, e
}

func parsePruneReported(output string, buildkit bool) (*uint64, error) {
	var reported *uint64
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		value, ok := strings.CutPrefix(line, "Total reclaimed space:")
		if !ok && buildkit {
			value, ok = strings.CutPrefix(line, "Total:")
		}
		if !ok {
			continue
		}
		n, e := ParseSize(strings.TrimSpace(value))
		if e != nil {
			return nil, fmt.Errorf("cannot parse provider-reported reclaimed space %q: %w", value, e)
		}
		reported = &n
	}
	return reported, nil
}

func parseDockerDate(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05 -0700"} {
		if t, e := time.Parse(layout, s); e == nil {
			return t
		}
	}
	return time.Time{}
}
