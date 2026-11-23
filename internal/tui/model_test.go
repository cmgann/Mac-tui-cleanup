package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cmgann/Mac-tui-cleanup/internal/config"
	"github.com/cmgann/Mac-tui-cleanup/internal/providers"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: []rune(s)[0], Text: s} }
func TestProtectedPathEditorPersistsAndDeselectsWithoutDeletion(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, "cache with spaces")
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, "state/settings.json")
	m := New(&storage.Engine{Home: home}, config.Defaults(), settingsPath)
	m.snapshot.Candidates = []storage.Candidate{{ID: "cache", Path: cache, Reclaimable: true, Selected: true}}
	m.page = "settings"
	m.settingsCursor = 4
	send := func(k tea.KeyPressMsg) { u, _ := m.Update(k); m = u.(Model) }
	send(tea.KeyPressMsg{Code: tea.KeyEnter})
	send(key("a"))
	for _, r := range "~/cache with spaces/future-symbols" {
		send(key(string(r)))
	}
	send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.settings.ProtectedPaths) != 0 || !m.snapshot.Candidates[0].Selected {
		t.Fatal("draft applied before save")
	}
	send(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	select {
	case msg := <-m.events:
		u, _ := m.Update(msg)
		m = u.(Model)
	case <-time.After(3 * time.Second):
		t.Fatal("protection save blocked")
	}
	m.Wait()
	loaded, err := config.Load(settingsPath)
	if err != nil || len(loaded.ProtectedPaths) != 1 || loaded.ProtectedPaths[0] != filepath.Join(cache, "future-symbols") {
		t.Fatal(loaded, err)
	}
	if !m.snapshot.Candidates[0].Protected || m.snapshot.Candidates[0].Selected {
		t.Fatal("new descendant protection did not invalidate selection")
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatal("protection editor deleted cache", err)
	}
}
func TestSelectionProtectionAndReview(t *testing.T) {
	m := New(&storage.Engine{}, config.Settings{CacheAgeDays: 30}, "unused")
	m.snapshot.Candidates = []storage.Candidate{{ID: "a", Provider: "Docker", Reclaimable: true, Risk: storage.Low}, {ID: "b", Provider: "Docker", Protected: true, Reclaimable: true}}
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

func TestDeviceSupportRequiresIndividualReviewAndSelection(t *testing.T) {
	m := New(&storage.Engine{}, config.Settings{}, "")
	m.category = "Xcode"
	m.page = "details"
	m.snapshot.Candidates = []storage.Candidate{{ID: "support", Provider: "Xcode", Category: "iOS DeviceSupport", Path: "/fixture/support", Reclaimable: true, ReviewRequired: true, Risk: storage.High}}
	send := func(k tea.KeyPressMsg) { t.Helper(); u, _ := m.Update(k); m = u.(Model) }
	send(key("a"))
	send(key(" "))
	if m.snapshot.Candidates[0].Selected {
		t.Fatal("unreviewed folder selected")
	}
	send(key("v"))
	if m.page != "support-review" {
		t.Fatal("review not opened")
	}
	for _, r := range "REVIE" {
		send(key(string(r)))
	}
	send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.snapshot.Candidates[0].Reviewed {
		t.Fatal("incomplete review accepted")
	}
	send(key("W"))
	send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.snapshot.Candidates[0].Reviewed || m.snapshot.Candidates[0].Selected || m.busy {
		t.Fatal("review deleted or selected candidate")
	}
	m.page = "candidates"
	send(key("a"))
	if m.snapshot.Candidates[0].Selected {
		t.Fatal("bulk selection included device support")
	}
	send(key(" "))
	if !m.snapshot.Candidates[0].Selected {
		t.Fatal("individual selection unavailable after review")
	}
	m.snapshot.Candidates[0].Protected = true
	send(key("v"))
	if m.page == "support-review" {
		t.Fatal("pin/protection bypassed by review")
	}
	u, _ := m.Update(scanDone{snapshot: storage.Snapshot{Candidates: []storage.Candidate{{ID: "support", Provider: "Xcode", ReviewRequired: true}}}})
	m = u.(Model)
	if m.snapshot.Candidates[0].Reviewed || m.snapshot.Candidates[0].Selected {
		t.Fatal("review or selection persisted across rescan")
	}
}

func TestFiltersDeselectHiddenItemsAndKeepHighRiskManual(t *testing.T) {
	m := New(&storage.Engine{}, config.Defaults(), filepath.Join(t.TempDir(), "settings.json"))
	m.category = "Xcode"
	m.page = "candidates"
	m.filterReturn = "candidates"
	m.snapshot.Candidates = []storage.Candidate{{ID: "small", Provider: "Xcode", Reclaimable: true, Selected: true, EstimatedBytes: 1000, Risk: storage.Low}, {ID: "large", Provider: "Xcode", Reclaimable: true, Selected: true, EstimatedBytes: 2000000000, Risk: storage.High}}
	m.filterDraft = m.settings.Filter
	m.filterDraft.MinBytes = 1000000000
	_ = m.applyFilters()
	select {
	case msg := <-m.events:
		u, _ := m.Update(msg)
		m = u.(Model)
	case <-time.After(3 * time.Second):
		t.Fatal("filter save blocked")
	}
	if m.snapshot.Candidates[0].Selected || !m.snapshot.Candidates[1].Selected || len(m.indices()) != 1 {
		t.Fatal("hidden selection survived", m.snapshot.Candidates)
	}
	m.snapshot.Candidates[1].Selected = false
	u, _ := m.Update(key("a"))
	m = u.(Model)
	if m.snapshot.Candidates[1].Selected {
		t.Fatal("high-risk item bulk selected")
	}
	u, _ = m.Update(key(" "))
	m = u.(Model)
	if !m.snapshot.Candidates[1].Selected {
		t.Fatal("explicit high-risk selection blocked")
	}
	loaded, e := config.Load(m.settingsPath)
	if e != nil || loaded.Filter.MinBytes != 1000000000 {
		t.Fatal(loaded, e)
	}
}
func TestLiveProgressRenderingAndRescanCancellation(t *testing.T) {
	m := New(&storage.Engine{}, config.Defaults(), "")
	m.busy = true
	m.page = "progress"
	m.cancel = func() {}
	send := func(msg tea.Msg) { u, _ := m.Update(msg); m = u.(Model) }
	send(cleanupProgressMsg(storage.ProgressEvent{Kind: "operation", Name: "Fixture operation", Index: 1, Total: 2}))
	send(cleanupProgressMsg(storage.ProgressEvent{Kind: "command", Text: "tool delete fixture", Total: 2}))
	send(cleanupProgressMsg(storage.ProgressEvent{Kind: "output", Text: "live-output\n\x1b]52;c;dGVzdA==\a", Total: 2}))
	send(cleanupProgressMsg(storage.ProgressEvent{Kind: "finished", Name: "Fixture operation", Status: "failed", Completed: 1, Failed: 1, Total: 2}))
	view := m.View().Content
	if !strings.Contains(view, "live-output") || !strings.Contains(view, "Failed 1") || strings.Contains(view, "\x1b]52") {
		t.Fatal("invalid live output rendering", view)
	}
	send(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.outputOffset == 0 {
		t.Fatal("live output cannot scroll")
	}
	canceled := false
	send(phaseMsg{status: "Rescanning", cancel: func() { canceled = true }})
	send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !canceled {
		t.Fatal("rescan cancellation used stale cleanup context")
	}
}
