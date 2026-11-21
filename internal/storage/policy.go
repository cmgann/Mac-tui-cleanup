package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Policy struct {
	Home                   string
	ProtectedPaths, Pinned []string
	StaleDays              int
}
type policyGuardKey struct{}

func WithCleanupGuard(ctx context.Context, guard func() error) context.Context {
	return context.WithValue(ctx, policyGuardKey{}, guard)
}
func CheckCleanup(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if guard, ok := ctx.Value(policyGuardKey{}).(func() error); ok && guard != nil {
		return guard()
	}
	return nil
}

// CanonicalPath resolves symlinks in the existing prefix, including protections
// for files/directories which do not exist yet. Paths are literal, never globs.
func CanonicalPath(path, home string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if path == "~" {
		path = home
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", fmt.Errorf("protected path must be an absolute path or ~/path: %q", path)
	}
	path = filepath.Clean(path)
	current := path
	var suffix []string
	for {
		_, e := os.Lstat(current)
		if e == nil {
			real, e := filepath.EvalSymlinks(current)
			if e != nil {
				return "", e
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				real = filepath.Join(real, suffix[i])
			}
			return real, nil
		}
		if !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", e
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}
func overlaps(a, b string) bool {
	if runtime.GOOS == "darwin" {
		a = strings.ToLower(a)
		b = strings.ToLower(b)
	}
	rel, e := filepath.Rel(a, b)
	if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	rel, e = filepath.Rel(b, a)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (p Policy) Check(c Candidate) error {
	for _, id := range p.Pinned {
		if id == c.ID {
			return errors.New("candidate is pinned")
		}
	}
	scopes := append([]string(nil), c.ScopePaths...)
	if c.Path != "" {
		scopes = append(scopes, c.Path)
	}
	for _, protected := range p.ProtectedPaths {
		root, e := CanonicalPath(protected, p.Home)
		if e != nil {
			return fmt.Errorf("invalid protection: %w", e)
		}
		for _, scope := range scopes {
			if scope == "" {
				continue
			}
			target, e := CanonicalPath(scope, p.Home)
			if e != nil {
				return fmt.Errorf("cannot validate cleanup scope: %w", e)
			}
			if overlaps(root, target) {
				return fmt.Errorf("protected path overlaps cleanup scope: %s", protected)
			}
		}
	}
	return nil
}
func (p Policy) Apply(cs []Candidate, now time.Time) {
	days := p.StaleDays
	if days <= 0 {
		days = 90
	}
	for i := range cs {
		c := &cs[i]
		if e := p.Check(*c); e != nil {
			c.Protected = true
			c.Selected = false
			c.ProtectionReason = e.Error()
		}
		c.Stale = false
		c.StaleReason = ""
		if c.Reclaimable && !c.Protected && !c.LastModified.IsZero() && now.Sub(c.LastModified) >= time.Duration(days)*24*time.Hour {
			c.Stale = true
			basis := c.Metadata["age_basis"]
			if basis == "" {
				basis = "last modified"
			}
			c.StaleReason = fmt.Sprintf("Old candidate: %s %s (at least %d days). Age is a review hint, not proof the resource is unused.", basis, c.LastModified.Format(time.RFC3339), days)
		}
	}
}

func (p Policy) Validate() error {
	for _, path := range p.ProtectedPaths {
		if _, e := CanonicalPath(path, p.Home); e != nil {
			return e
		}
	}
	return nil
}
