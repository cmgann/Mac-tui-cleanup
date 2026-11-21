package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type Settings struct {
	CacheAgeDays int
	Pinned       []string
}

func Directory() (string, error) { d, e := os.UserConfigDir(); return filepath.Join(d, "devclean"), e }
func Load(path string) (Settings, error) {
	s := Settings{CacheAgeDays: 30}
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
	return s, e
}
func Save(path string, s Settings) error { return storage.AtomicJSON(path, s) }
