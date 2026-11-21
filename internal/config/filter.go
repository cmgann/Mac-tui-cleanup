package config

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type Filter struct {
	Query, Category  string
	MinBytes         uint64
	OlderThanDays    int
	MaxRisk          storage.Risk
	Visibility, Sort string
}

func RiskRank(r storage.Risk) int {
	switch r {
	case storage.Low:
		return 1
	case storage.Medium:
		return 2
	default:
		return 3
	}
}
func (f Filter) Matches(c storage.Candidate, now time.Time) bool {
	if f.Query != "" && !strings.Contains(strings.ToLower(c.Name+" "+c.Path+" "+c.Category+" "+c.Provider+" "+c.Description), strings.ToLower(f.Query)) {
		return false
	}
	if f.Category != "" && !strings.Contains(strings.ToLower(c.Category), strings.ToLower(f.Category)) {
		return false
	}
	if c.EstimatedBytes < f.MinBytes {
		return false
	}
	if f.OlderThanDays > 0 && (c.LastModified.IsZero() || now.Sub(c.LastModified) < time.Duration(f.OlderThanDays)*24*time.Hour) {
		return false
	}
	if f.MaxRisk != "" && c.Reclaimable && RiskRank(c.Risk) > RiskRank(f.MaxRisk) {
		return false
	}
	switch f.Visibility {
	case "actionable":
		if !c.Reclaimable || c.Protected {
			return false
		}
	case "eligible":
		if !c.Eligible() {
			return false
		}
	}
	return true
}
func (f Filter) SortIndices(cs []storage.Candidate, ids []int) {
	sort.SliceStable(ids, func(i, j int) bool {
		a, b := cs[ids[i]], cs[ids[j]]
		switch f.Sort {
		case "size":
			if a.EstimatedBytes != b.EstimatedBytes {
				return a.EstimatedBytes > b.EstimatedBytes
			}
		case "modified":
			if !a.LastModified.Equal(b.LastModified) {
				return a.LastModified.After(b.LastModified)
			}
		case "risk":
			if RiskRank(a.Risk) != RiskRank(b.Risk) {
				return RiskRank(a.Risk) < RiskRank(b.Risk)
			}
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}
func (f Filter) Summary() string {
	var parts []string
	if f.Query != "" {
		parts = append(parts, "text="+f.Query)
	}
	if f.Category != "" {
		parts = append(parts, "category="+f.Category)
	}
	if f.MinBytes > 0 {
		parts = append(parts, "size≥"+storage.Bytes(f.MinBytes))
	}
	if f.OlderThanDays > 0 {
		parts = append(parts, fmt.Sprintf("modified≥%dd", f.OlderThanDays))
	}
	if f.MaxRisk != "" {
		parts = append(parts, "risk≤"+string(f.MaxRisk))
	}
	if f.Visibility != "" && f.Visibility != "all" {
		parts = append(parts, f.Visibility)
	}
	if len(parts) == 0 {
		return "all candidates"
	}
	return strings.Join(parts, " · ")
}
