package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type Settings struct {
	CacheAgeDays      int
	Pinned            []string
	Filter            Filter
	BulkMaxRisk       storage.Risk
	ShowCommandOutput bool
	OutputLines       int
}

func Directory() (string, error) { d, e := os.UserConfigDir(); return filepath.Join(d, "devclean"), e }
func Load(path string) (Settings, error) {
	s := Defaults()
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(b, &s)
	if s.CacheAgeDays != 0 && s.CacheAgeDays != 7 && s.CacheAgeDays != 30 {
		s.CacheAgeDays = 30
	}
	s = Normalize(s)
	return s, e
}
func Save(path string, s Settings) error { return storage.AtomicJSON(path, s) }

func Defaults() Settings {
	return Settings{CacheAgeDays: 30, BulkMaxRisk: storage.Medium, ShowCommandOutput: true, OutputLines: 100, Filter: Filter{Sort: "size", Visibility: "all"}}
}
func Normalize(s Settings) Settings {
	if s.BulkMaxRisk != storage.Low && s.BulkMaxRisk != storage.Medium && s.BulkMaxRisk != storage.High {
		s.BulkMaxRisk = storage.Medium
	}
	if s.OutputLines != 50 && s.OutputLines != 100 && s.OutputLines != 200 {
		s.OutputLines = 100
	}
	if s.Filter.OlderThanDays < 0 || s.Filter.OlderThanDays > 3650 {
		s.Filter.OlderThanDays = 0
	}
	switch s.Filter.Sort {
	case "name", "size", "modified", "risk":
	default:
		s.Filter.Sort = "size"
	}
	switch s.Filter.Visibility {
	case "all", "actionable", "eligible":
	default:
		s.Filter.Visibility = "all"
	}
	if s.Filter.MaxRisk != storage.Low && s.Filter.MaxRisk != storage.Medium && s.Filter.MaxRisk != storage.High {
		s.Filter.MaxRisk = ""
	}
	s.Filter.Query = string([]rune(s.Filter.Query)[:min(128, len([]rune(s.Filter.Query)))])
	s.Filter.Category = string([]rune(s.Filter.Category)[:min(80, len([]rune(s.Filter.Category)))])
	return s
}
