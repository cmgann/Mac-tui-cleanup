package tui

import (
	"context"
	"github.com/cmgann/Mac-tui-cleanup/internal/providers"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cmgann/Mac-tui-cleanup/internal/config"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: []rune(s)[0], Text: s} }
func TestSelectionProtectionAndReview(t *testing.T) {
	m := New(&storage.Engine{}, config.Settings{CacheAgeDays: 30}, "unused")
	m.snapshot.Candidates = []storage.Candidate{{ID: "a", Provider: "Docker", Reclaimable: true}, {ID: "b", Provider: "Docker", Protected: true, Reclaimable: true}}
	updated, _ := m.Update(key("a"))
	m = updated.(Model)
	if !m.snapshot.Candidates[0].Selected || m.snapshot.Candidates[1].Selected {
		t.Fatal("protection ignored")
	}
	updated, _ = m.Update(planDone{ops: []storage.Operation{{Scope: "test-only"}}})
	m = updated.(Model)
	if m.page != "review" {
		t.Fatal(m.page)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.page != "confirm" || m.busy {
		t.Fatal("cleanup started without typed confirmation")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if updated.(Model).busy {
		t.Fatal("empty confirmation accepted")
	}
}
func TestSafeRenderingAndResize(t *testing.T) {
	if safe("safe\x1b[31mred\x1b[0m\x00") != "safered" {
		t.Fatal("terminal controls retained")
	}
	m := New(&storage.Engine{}, config.Settings{}, "")
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 40, Height: 18}, {Width: 1, Height: 1}} {
		u, _ := m.Update(size)
		m = u.(Model)
		if v := m.View(); !strings.Contains(v.Content, "devclean") && size.Width > 40 {
			t.Fatal("missing title")
		}
	}
}

type fixtureRunner struct{}

func (fixtureRunner) Run(_ context.Context, _ string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "-axo" {
		return "", nil
	}
	return `{}`, nil
}
func TestConfirmedCleanupEndToEnd(t *testing.T) {
	home, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(home, "Library/Developer/Xcode/DerivedData/Fixture Project")
	if e = os.MkdirAll(path, 0700); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(path, "build-cache"), make([]byte, 16384), 0600)
	provider := &providers.Xcode{Home: home, Run: fixtureRunner{}}
	engine := &storage.Engine{Home: home, History: storage.History{Path: filepath.Join(home, "state/history.json")}, Providers: []storage.Provider{provider}}
	m := New(engine, config.Settings{CacheAgeDays: 30}, filepath.Join(home, "settings.json"))
	m.snapshot = engine.Scan(context.Background())
	for i := range m.snapshot.Candidates {
		if m.snapshot.Candidates[i].Path == path {
			m.snapshot.Candidates[i].Selected = true
		}
	}
	u, _ := m.Update(key("c"))
	m = u.(Model)
	select {
	case msg := <-m.events:
		u, _ = m.Update(msg)
		m = u.(Model)
	case <-time.After(3 * time.Second):
		t.Fatal("plan blocked event loop")
	}
	if m.page != "review" || m.busy {
		t.Fatal(m.page)
	}
	u, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = u.(Model)
	for _, r := range "DELET" {
		u, _ = m.Update(key(string(r)))
		m = u.(Model)
	}
	u, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = u.(Model)
	if _, e = os.Stat(path); e != nil {
		t.Fatal("deleted before full confirmation")
	}
	u, _ = m.Update(key("E"))
	m = u.(Model)
	u, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = u.(Model)
	deadline := time.After(5 * time.Second)
	for m.busy {
		select {
		case msg := <-m.events:
			u, _ = m.Update(msg)
			m = u.(Model)
		case <-deadline:
			t.Fatal("cleanup did not complete")
		}
	}
	m.Wait()
	if m.page != "results" || len(m.results) != 1 || !m.results[0].Success {
		t.Fatal(m.page, m.results)
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("confirmed temporary candidate not removed", e)
	}
	rows, e := engine.History.Load()
	if e != nil || len(rows) != 1 || !rows[0].MeasurementValid {
		t.Fatal(rows, e)
	}
	for _, c := range m.snapshot.Candidates {
		if c.Path == path {
			t.Fatal("cleanup did not rescan")
		}
	}
}
