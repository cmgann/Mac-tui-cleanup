//go:build !darwin && !linux

package storage

import "errors"

func DiskUsage(string) (Disk, error) {
	return Disk{}, errors.New("devclean currently supports macOS only")
}
