package storage

import "syscall"

func DiskUsage(path string) (Disk, error) {
	var s syscall.Statfs_t
	if e := syscall.Statfs(path, &s); e != nil {
		return Disk{}, e
	}
	b := uint64(s.Bsize)
	return Disk{Total: s.Blocks * b, Used: (s.Blocks - s.Bfree) * b, Available: s.Bavail * b}, nil
}
