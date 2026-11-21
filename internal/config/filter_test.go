package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

func TestCandidateFiltersAndUnknownMeasurements(t *testing.T) {
	now := time.Now()
	c := storage.Candidate{ID: "x", Name: "Fixture App", Category: "DerivedData", Provider: "Xcode", EstimatedBytes: 2000000000, Risk: storage.Medium, Reclaimable: true, LastModified: now.Add(-40 * 24 * time.Hour)}
	f := Filter{Query: "fixture", Category: "derived", MinBytes: 1000000000, OlderThanDays: 30, MaxRisk: storage.Medium, Visibility: "eligible"}
	if !f.Matches(c, now) {
		t.Fatal("matching candidate hidden")
	}
	for _, change := range []func(*storage.Candidate){func(c *storage.Candidate) { c.EstimatedBytes = 0 }, func(c *storage.Candidate) { c.LastModified = time.Time{} }, func(c *storage.Candidate) { c.Risk = storage.High }, func(c *storage.Candidate) { c.Protected = true }, func(c *storage.Candidate) { c.ReviewRequired = true }} {
		other := c
		change(&other)
		if f.Matches(other, now) {
			t.Fatal("filter accepted nonmatching candidate", other)
		}
	}
	f.Visibility = "actionable"
	c.ReviewRequired = true
	if !f.Matches(c, now) {
		t.Fatal("pending individual review should remain visible in actionable mode")
	}
}
func TestPreferencesPersistAndMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := Defaults()
	s.Filter = Filter{Query: "a project", Category: "BuildKit", MinBytes: 1000, OlderThanDays: 90, MaxRisk: storage.Low, Visibility: "actionable", Sort: "risk"}
	s.BulkMaxRisk = storage.Low
	s.OutputLines = 200
	s.ShowCommandOutput = false
	if e := Save(path, s); e != nil {
		t.Fatal(e)
	}
	got, e := Load(path)
	if e != nil || got.Filter != s.Filter || got.ShowCommandOutput || got.BulkMaxRisk != storage.Low || got.OutputLines != 200 {
		t.Fatal(got, e)
	}
	normalized := Normalize(Settings{Filter: Filter{OlderThanDays: -1, MaxRisk: "invalid", Sort: "invalid", Query: string(make([]rune, 200))}})
	if normalized.Filter.OlderThanDays != 0 || normalized.Filter.MaxRisk != "" || len([]rune(normalized.Filter.Query)) != 128 || normalized.OutputLines != 100 {
		t.Fatal(normalized)
	}
}
