package providers

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type Homebrew struct {
	Run        runner.Runner
	Executable string
}

func (*Homebrew) ID() string { return "Homebrew" }
func (b *Homebrew) executable() (string, error) {
	if b.Executable != "" {
		return b.Executable, nil
	}
	return exec.LookPath("brew")
}

var brewSavings = regexp.MustCompile(`(?i)(?:would free|freed|free approximately|would free approximately)\s+([0-9.]+\s*[kMGT]?i?B)`)

func (b *Homebrew) Scan(ctx context.Context) ([]storage.Candidate, error) {
	exe, e := b.executable()
	if e != nil {
		return nil, e
	}
	var cs []storage.Candidate
	roots := map[string]string{}
	var scopes []string
	var errs []error
	for _, flag := range []string{"--prefix", "--cache"} {
		out, e := b.Run.Run(ctx, exe, flag)
		if e != nil {
			errs = append(errs, e)
			continue
		}
		path := strings.TrimSpace(out)
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			errs = append(errs, errors.New("Homebrew did not return a clean absolute path"))
			continue
		}
		roots[flag] = path
		scopes = append(scopes, path)
		n, e := storage.TreeBytes(ctx, path)
		if e != nil {
			errs = append(errs, e)
		}
		cs = append(cs, storage.Candidate{ID: "brew:" + flag, Provider: b.ID(), Category: "Inspection", Name: "Homebrew " + strings.TrimPrefix(flag, "--"), Path: path, EstimatedBytes: n, Protected: true, ProtectionReason: "Managed by Homebrew; no direct directory deletion"})
	}
	out, e := b.Run.Run(ctx, exe, "cleanup", "--dry-run")
	if e != nil {
		errs = append(errs, e)
	} else {
		var n uint64
		if m := brewSavings.FindStringSubmatch(out); len(m) > 1 {
			n, _ = ParseSize(m[1])
		}
		cs = append(cs, storage.Candidate{ID: "brew:cleanup", Provider: b.ID(), Category: "Cleanup", Name: "Clean old Homebrew versions and downloads", Description: out, EstimatedBytes: n, Reclaimable: true, Risk: storage.Medium, ScopePaths: scopes, Metadata: map[string]string{"executable": exe, "dryrun": out, "prefix": roots["--prefix"], "cache": roots["--cache"]}})
	}
	return cs, errors.Join(errs...)
}
func (b *Homebrew) Plan(ctx context.Context, c storage.Candidate, _ int) (storage.Operation, error) {
	if c.ID != "brew:cleanup" || !c.Eligible() || c.Metadata["prefix"] == "" || c.Metadata["cache"] == "" {
		return storage.Operation{}, errors.New("unsupported Homebrew action")
	}
	return storage.Operation{Candidate: c, Scope: c.Metadata["executable"] + " cleanup", Warning: "Homebrew removes old installed versions and stale downloads. Current packages, active services and databases are retained. Dry-run output:\n" + c.Metadata["dryrun"]}, nil
}
func (b *Homebrew) Clean(ctx context.Context, op storage.Operation) (storage.Result, error) {
	if _, e := b.Plan(ctx, op.Candidate, 0); e != nil {
		return storage.Result{}, e
	}
	exe, e := b.executable()
	if e != nil {
		return storage.Result{}, e
	}
	m := op.Candidate.Metadata
	if exe != m["executable"] {
		return storage.Result{}, errors.New("Homebrew executable changed")
	}
	for flag, key := range map[string]string{"--prefix": "prefix", "--cache": "cache"} {
		out, e := b.Run.Run(ctx, exe, flag)
		if e != nil {
			return storage.Result{}, e
		}
		if strings.TrimSpace(out) != m[key] {
			return storage.Result{}, errors.New("Homebrew cleanup scope changed")
		}
	}
	out, e := b.Run.Run(ctx, exe, "cleanup", "--dry-run")
	if e != nil {
		return storage.Result{}, e
	}
	if out != m["dryrun"] {
		return storage.Result{}, errors.New("Homebrew cleanup scope changed; rescan and review again")
	}
	out, e = b.Run.Run(ctx, exe, "cleanup")
	result := storage.Result{Output: out}
	if match := brewSavings.FindStringSubmatch(out); len(match) > 1 {
		if n, err := ParseSize(match[1]); err == nil {
			result.ProviderReportedBytes = &n
		}
	}
	return result, e
}
