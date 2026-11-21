package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// TreeBytes measures allocated blocks, never following symlinks or other mounts.
func TreeBytes(ctx context.Context, path string) (uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	base, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("filesystem metadata unavailable")
	}
	var n uint64
	seen := map[[2]uint64]bool{}
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		st := i.Sys().(*syscall.Stat_t)
		if st.Dev != base.Dev {
			return fmt.Errorf("unexpected mount at %s", p)
		}
		key := [2]uint64{uint64(st.Dev), st.Ino}
		if !seen[key] {
			seen[key] = true
			n += uint64(max(st.Blocks, 0)) * 512
		}
		return nil
	})
	return n, err
}

// ValidateTarget permits only a direct child of a trusted root. Every component
// and descendant must be a regular file/directory on the same device.
func ValidateTarget(ctx context.Context, root, target string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || filepath.Dir(target) != root || filepath.Clean(target) != target {
		return errors.New("target is outside approved direct-child scope")
	}
	real, e := filepath.EvalSymlinks(root)
	if e != nil {
		return e
	}
	if real != root {
		return errors.New("cleanup root contains symlinks")
	}
	r, e := os.Stat(root)
	if e != nil {
		return e
	}
	dev := uint64(r.Sys().(*syscall.Stat_t).Dev)
	return filepath.WalkDir(target, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink protected: %s", p)
		}
		if d.Name() == ".git" || d.Name() == ".hg" || d.Name() == ".svn" {
			return errors.New("source repository protected")
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		if !i.IsDir() && !i.Mode().IsRegular() {
			return errors.New("special filesystem entry protected")
		}
		st := i.Sys().(*syscall.Stat_t)
		if uint64(st.Dev) != dev {
			return errors.New("mounted filesystem protected")
		}
		return nil
	})
}
func DeleteChild(ctx context.Context, root, target string, expected ...os.FileInfo) error {
	return deleteChild(ctx, root, target, expected, nil)
}

// DeleteChildGuarded runs the fresh resource-eligibility check after filesystem
// validation, so a slow safety walk cannot stale the device inventory.
func DeleteChildGuarded(ctx context.Context, root, target string, expected os.FileInfo, guard func(context.Context) error) error {
	return deleteChild(ctx, root, target, []os.FileInfo{expected}, guard)
}
func deleteChild(ctx context.Context, root, target string, expected []os.FileInfo, guard func(context.Context) error) error {
	trustedRoot, e := os.Stat(root)
	if e != nil {
		return e
	}
	if len(expected) == 0 {
		info, e := os.Lstat(target)
		if e != nil {
			return e
		}
		expected = []os.FileInfo{info}
	}
	if e := ValidateTarget(ctx, root, target); e != nil {
		return e
	}
	r, e := os.OpenRoot(root)
	if e != nil {
		return e
	}
	defer r.Close()
	// os.Root prevents symlink traversal outside the approved root, including races.
	info, e := r.Stat(".")
	if e != nil {
		return e
	}
	if !os.SameFile(trustedRoot, info) {
		return errors.New("cleanup root changed during validation")
	}
	if guard != nil {
		if e = guard(ctx); e != nil {
			return e
		}
	}
	if e = CheckCleanup(ctx); e != nil {
		return e
	}
	return removeTree(ctx, r, filepath.Base(target), uint64(info.Sys().(*syscall.Stat_t).Dev), expected...)
}
func removeTree(ctx context.Context, parent *os.Root, name string, dev uint64, expected ...os.FileInfo) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if name == ".git" || name == ".hg" || name == ".svn" {
		return errors.New("repository marker protected")
	}
	i, e := parent.Lstat(name)
	if e != nil {
		return e
	}
	if len(expected) > 0 && !os.SameFile(expected[0], i) {
		return errors.New("target changed during validation")
	}
	if i.Mode()&os.ModeSymlink != 0 {
		return errors.New("target changed to symlink")
	}
	if uint64(i.Sys().(*syscall.Stat_t).Dev) != dev {
		return errors.New("mounted filesystem protected")
	}
	if !i.IsDir() && !i.Mode().IsRegular() {
		return errors.New("special filesystem entry protected")
	}
	if i.IsDir() {
		child, e := parent.OpenRoot(name)
		if e != nil {
			return e
		}
		defer child.Close()
		opened, e := child.Stat(".")
		if e != nil {
			return e
		}
		if !os.SameFile(i, opened) {
			return errors.New("directory changed during cleanup")
		}
		f, e := child.Open(".")
		if e != nil {
			return e
		}
		entries, e := f.ReadDir(-1)
		f.Close()
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if e = removeTree(ctx, child, entry.Name(), dev); e != nil {
				return e
			}
		}
	}
	return parent.Remove(name)
}
