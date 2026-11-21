package storage

import (
	"context"
	"fmt"
	"time"
)

type Risk string

const (
	Low    Risk = "low"
	Medium Risk = "medium"
	High   Risk = "high"
)

type DeviceSupportInfo struct {
	Platform, Version, Build, DeviceIdentifier string
	MatchedDeviceIDs                           []string
	InventorySources                           []string
	InventoryAt                                time.Time
	Recent                                     bool
}

type Candidate struct {
	ID, Provider, Category, Name, Description, Path string
	EstimatedBytes                                  uint64
	Risk                                            Risk
	Reclaimable, Protected, Selected                bool
	ProtectionReason                                string
	LastModified                                    time.Time
	Metadata                                        map[string]string
	ReviewRequired, Reviewed                        bool
	DeviceSupport                                   *DeviceSupportInfo
}

func (c Candidate) Eligible() bool {
	return c.Reclaimable && !c.Protected && (!c.ReviewRequired || c.Reviewed)
}

type Operation struct {
	Candidate      Candidate
	Scope, Warning string
	Args           []string
	AgeDays        int
}
type ProgressEvent struct {
	Kind, Provider, CandidateID, Name, Status, Stream, Text string
	Index, Total, Completed, Failed                         int
	At                                                      time.Time
}
type Result struct {
	ProviderReportedBytes                             *uint64
	ProviderUsageBeforeBytes, ProviderUsageAfterBytes *uint64
	MeasurementErrors                                 []string
	Output                                            string
}
type Provider interface {
	ID() string
	Scan(context.Context) ([]Candidate, error)
	Plan(context.Context, Candidate, int) (Operation, error)
	Clean(context.Context, Operation) (Result, error)
}
type Snapshot struct {
	Candidates []Candidate
	Errors     []string
	Disk       Disk
	At         time.Time
	Canceled   bool
}
type Disk struct{ Total, Used, Available uint64 }

func (d Disk) Percent() float64 {
	if d.Total == 0 {
		return 0
	}
	return 100 * float64(d.Used) / float64(d.Total)
}
func Bytes(n uint64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	units := []string{"kB", "MB", "GB", "TB", "PB", "EB"}
	for _, u := range units {
		v /= 1000
		if v < 1000 || u == "EB" {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return "?"
}
func Signed(n int64) string {
	if n < 0 {
		return "−" + Bytes(uint64(-(n+1))+1)
	}
	return "+" + Bytes(uint64(n))
}
func Delta(before, after uint64) int64 {
	if after >= before {
		return int64(after - before)
	}
	return -int64(before - after)
}
func Estimate(cs []Candidate) uint64 {
	var n uint64
	seen := map[string]bool{}
	for _, c := range cs {
		if !c.Eligible() {
			continue
		}
		key := c.ID
		if c.Metadata["accounting"] != "" {
			key = c.Metadata["accounting"]
		}
		if !seen[key] {
			n += c.EstimatedBytes
			seen[key] = true
		}
	}
	return n
}
