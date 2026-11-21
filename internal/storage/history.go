package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Record struct {
	ID                                                string
	Timestamp                                         time.Time
	Provider, Operation                               string
	CandidateIDs                                      []string
	EstimatedBytes                                    uint64
	ProviderReportedBytes                             *uint64
	ProviderUsageBeforeBytes, ProviderUsageAfterBytes *uint64
	PlanWarning                                       string
	DiskDeltaBytes                                    int64
	AvailableBefore, AvailableAfter                   uint64
	MeasurementValid                                  bool
	Success                                           bool
	Errors                                            []string
	Output                                            string
	CommandOutput                                     string
}
type History struct{ Path string }

func (h History) Load() ([]Record, error) {
	b, e := os.ReadFile(h.Path)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var r []Record
	if e = json.Unmarshal(b, &r); e != nil {
		return nil, fmt.Errorf("history is corrupt; preserved at %s: %w", h.Path, e)
	}
	return r, nil
}
func AtomicJSON(path string, v any) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".devclean-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), path)
}
func (h History) Append(record Record) error {
	unlock, e := FileLock(h.Path + ".lock")
	if e != nil {
		return e
	}
	defer unlock()
	rs, e := h.Load()
	if e != nil {
		return e
	}
	return AtomicJSON(h.Path, append(rs, record))
}
func Lifetime(rs []Record) int64 {
	var n int64
	for _, r := range rs {
		if r.MeasurementValid {
			n += r.DiskDeltaBytes
		}
	}
	return n
}

// FileLock prevents concurrent cleaners and lost history updates.
func FileLock(path string) (func(), error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("another devclean process holds the lock: %w", e)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
