package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsPersistenceAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, e := Load(path)
	if e != nil || s.CacheAgeDays != 30 {
		t.Fatal(s, e)
	}
	s.CacheAgeDays = 7
	s.Pinned = []string{"xcode:project"}
	if e = Save(path, s); e != nil {
		t.Fatal(e)
	}
	got, e := Load(path)
	if e != nil || got.CacheAgeDays != 7 || len(got.Pinned) != 1 {
		t.Fatal(got, e)
	}
	os.WriteFile(path, []byte(`{"CacheAgeDays":999}`), 0600)
	got, e = Load(path)
	if e != nil || got.CacheAgeDays != 30 {
		t.Fatal(got, e)
	}
	os.WriteFile(path, []byte(`bad`), 0600)
	if _, e = Load(path); e == nil {
		t.Fatal("corrupt settings hidden")
	}
}
