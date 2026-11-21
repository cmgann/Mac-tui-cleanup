package storage

import "syscall"

func DiskUsage(path string) (Disk, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return Disk{}, err
	}
	return Disk{Total: s.Blocks * uint64(s.Bsize), Used: (s.Blocks - s.Bfree) * uint64(s.Bsize), Available: s.Bavail * uint64(s.Bsize)}, nil
}
