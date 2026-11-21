package tui

import (
	"context"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/cmgann/Mac-tui-cleanup/internal/config"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type progressMsg string
type cleanupProgressMsg storage.ProgressEvent
type phaseMsg struct {
	status string
	cancel context.CancelFunc
}
type diskMsg storage.Disk
type scanDone struct {
	snapshot storage.Snapshot
	history  []storage.Record
	err      error
}
type planDone struct {
	ops []storage.Operation
	err error
}
type cleanDone struct {
	records  []storage.Record
	snapshot storage.Snapshot
	history  []storage.Record
	err      error
}
type savedMsg struct {
	err    error
	status string
}
type Model struct {
	progressTab                            int
	lastOutputStream                       string
	filterDraft                            config.Filter
	filterCursor, settingsCursor           int
	filterEditing                          bool
	filterReturn                           string
	liveOutput, lastCommand, operationName string
	outputOffset, completed, failed, total int
	operationStarted                       time.Time
	completedOperations                    []string

	reviewID                      string
	engine                        *storage.Engine
	settings                      config.Settings
	settingsPath                  string
	snapshot                      storage.Snapshot
	history, results              []storage.Record
	ops                           []storage.Operation
	page, category, status, typed string
	cursor, offset, width, height int
	spinner                       spinner.Model
	busy                          bool
	started                       time.Time
	cancel                        context.CancelFunc
	events                        chan tea.Msg
	session                       int64
	ctx                           context.Context
	workers                       *sync.WaitGroup
}

func New(e *storage.Engine, s config.Settings, path string, contexts ...context.Context) Model {
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	s = config.Normalize(s)
	return Model{ctx: ctx, workers: &sync.WaitGroup{}, engine: e, settings: s, settingsPath: path, page: "dashboard", width: 88, height: 28, spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)), events: make(chan tea.Msg, 32)}
}
func (m Model) Init() tea.Cmd { return func() tea.Msg { return progressMsg("initial") } }
func (m Model) wait() tea.Cmd { ch := m.events; return func() tea.Msg { return <-ch } }
func (m *Model) start(kind string, work func(context.Context, func(string)) tea.Msg) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.busy = true
	m.started = time.Now()
	m.status = kind
	ch := m.events
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		defer cancel()
		msg := work(ctx, func(s string) {
			select {
			case ch <- progressMsg(s):
			default:
			}
		})
		select {
		case ch <- msg:
		case <-m.ctx.Done():
		}
	}()
	return tea.Batch(m.wait(), m.spinner.Tick)
}
func (m Model) Wait() { m.workers.Wait() }
func (m *Model) scan() tea.Cmd {
	e := m.engine
	ch := m.events
	return m.start("Scanning developer storage", func(ctx context.Context, p func(string)) tea.Msg {
		ctx, c := context.WithTimeout(ctx, 6*time.Minute)
		defer c()
		if d, err := storage.DiskUsage(e.Home); err == nil {
			ch <- diskMsg(d)
		}
		s := e.ScanProgress(ctx, p)
		rs, err := e.History.Load()
		return scanDone{s, rs, err}
	})
}
func (m *Model) applyPins() {
	for i := range m.snapshot.Candidates {
		c := &m.snapshot.Candidates[i]
		for _, id := range m.settings.Pinned {
			if id == c.ID {
				c.Protected = true
				c.Selected = false
				c.ProtectionReason = "Pinned in settings"
			}
		}
	}
}
func (m Model) indices() []int {
	var ids []int
	for i, c := range m.snapshot.Candidates {
		if c.Provider == m.category && m.visible(c) {
			ids = append(ids, i)
		}
	}
	m.settings.Filter.SortIndices(m.snapshot.Candidates, ids)
	return ids
}
func (m Model) categories() []string {
	return []string{"Docker", "Xcode", "Homebrew", "Other Developer"}
}
func (m Model) selected() []storage.Candidate {
	var cs []storage.Candidate
	for _, c := range m.snapshot.Candidates {
		if c.Selected {
			cs = append(cs, c)
		}
	}
	return cs
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(v.Width, 1)
		m.height = max(v.Height, 1)
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(v)
		if m.busy {
			return m, cmd
		}
		return m, nil
	case progressMsg:
		if v == "initial" {
			return m, m.scan()
		}
		m.status = string(v)
		return m, m.wait()
	case phaseMsg:
		m.cancel = v.cancel
		m.status = v.status
		return m, m.wait()
	case cleanupProgressMsg:
		event := storage.ProgressEvent(v)
		m.completed = event.Completed
		m.failed = event.Failed
		m.total = event.Total
		switch event.Kind {
		case "operation":
			m.operationName = event.Name
			m.operationStarted = time.Now()
			m.status = "Cleaning " + event.Name
			m.lastCommand = ""
			m.appendLive("\n--- " + event.Name + " ---\n")
		case "command":
			m.lastCommand = event.Text
			m.lastOutputStream = ""
			m.appendLive("\n$ " + event.Text)
		case "output":
			if m.settings.ShowCommandOutput {
				if event.Stream != m.lastOutputStream {
					m.appendLive("\n[" + event.Stream + "]\n")
					m.lastOutputStream = event.Stream
				}
				m.appendLive(event.Text)
			}
		case "finished":
			m.completedOperations = append(m.completedOperations, event.Name+": "+event.Status)
			if len(m.completedOperations) > 200 {
				m.completedOperations = m.completedOperations[len(m.completedOperations)-200:]
			}
			m.appendLive("\n[" + event.Status + "] " + event.Text + "\n")
		}
		return m, m.wait()
	case diskMsg:
		m.snapshot.Disk = storage.Disk(v)
		return m, m.wait()
	case scanDone:
		m.busy = false
		m.snapshot = v.snapshot
		m.history = v.history
		m.applyPins()
		m.cursor = 0
		m.offset = 0
		m.status = "Scan complete"
		if m.snapshot.Canceled {
			m.status = "Scan canceled; partial results"
		} else if len(m.snapshot.Errors) > 0 {
			m.status = "Scan finished with warnings · W opens details"
		}
		if v.err != nil {
			m.status = v.err.Error()
		}
		return m, nil
	case planDone:
		m.busy = false
		if v.err != nil {
			m.status = v.err.Error()
			return m, nil
		}
		m.ops = v.ops
		m.page = "review"
		m.offset = 0
		return m, nil
	case cleanDone:
		m.busy = false
		m.results = v.records
		m.snapshot = v.snapshot
		m.history = v.history
		m.applyPins()
		for _, r := range v.records {
			if r.MeasurementValid {
				m.session += r.DiskDeltaBytes
			}
		}
		m.page = "results"
		m.offset = 0
		m.status = "Cleanup completed; storage rescanned"
		if v.err != nil {
			m.status = v.err.Error()
		}
		return m, nil
	case savedMsg:
		m.busy = false
		m.status = v.status
		if v.err != nil {
			m.status = v.err.Error()
		}
		return m, nil
	case tea.PasteMsg:
		if m.page == "filters" && m.filterEditing {
			m.typed += strings.ReplaceAll(strings.ReplaceAll(v.Content, "\n", " "), "\r", " ")
			r := []rune(m.typed)
			m.typed = string(r[:min(128, len(r))])
		}
		return m, nil
	case tea.KeyPressMsg:
		k := v.String()
		if m.busy {
			if m.page == "progress" {
				switch k {
				case "up", "k":
					m.outputOffset++
				case "down", "j":
					m.outputOffset = max(0, m.outputOffset-1)
				case "pgup":
					m.outputOffset += max(1, m.height-15)
				case "pgdown":
					m.outputOffset = max(0, m.outputOffset-max(1, m.height-15))
				case "end":
					m.outputOffset = 0
				case "tab":
					m.progressTab = 1 - m.progressTab
					m.outputOffset = 0
				}
			}
			if k == "esc" || k == "ctrl+c" {
				m.cancel()
				m.status = "Cancellation requested; waiting for partial results"
			}
			return m, nil
		}
		if k == "ctrl+c" {
			return m, tea.Quit
		}
		if handled, cmd := m.preferenceKey(k); handled {
			return m, cmd
		}
		if m.page == "support-review" {
			switch k {
			case "up", "k":
				m.offset = max(0, m.offset-1)
			case "down", "j":
				m.offset++
			case "pgdown":
				m.offset += max(1, m.height-8)
			case "pgup":
				m.offset = max(0, m.offset-max(1, m.height-8))
			case "esc":
				m.page = "details"
				m.typed = ""
				m.reviewID = ""
			case "backspace":
				if len(m.typed) > 0 {
					m.typed = m.typed[:len(m.typed)-1]
				}
			case "enter":
				if m.typed == "REVIEW" {
					for i := range m.snapshot.Candidates {
						c := &m.snapshot.Candidates[i]
						if c.ID == m.reviewID && c.ReviewRequired && !c.Protected {
							c.Reviewed = true
						}
					}
					m.page = "details"
					m.status = "Review acknowledged for this scan. Space selects this resource; C reviews the deletion plan."
					m.typed = ""
					m.reviewID = ""
				}
			default:
				if len(k) == 1 && len(m.typed) < 6 {
					m.typed += k
				}
			}
			return m, nil
		}
		if m.page == "confirm" {
			switch k {
			case "esc":
				m.page = "review"
				m.typed = ""
			case "backspace":
				if len(m.typed) > 0 {
					m.typed = m.typed[:len(m.typed)-1]
				}
			case "enter":
				if m.typed == "DELETE" {
					e := m.engine
					ops := append([]storage.Operation(nil), m.ops...)
					m.page = "progress"
					m.progressTab = 0
					m.lastOutputStream = ""
					m.offset = 0
					m.liveOutput = ""
					m.lastCommand = ""
					m.operationName = ""
					m.outputOffset = 0
					m.completed = 0
					m.failed = 0
					m.total = len(ops)
					m.completedOperations = nil
					m.typed = ""
					return m, m.start("Cleaning selected resources", func(ctx context.Context, p func(string)) tea.Msg {
						rs := e.ExecuteProgress(ctx, ops, func(event storage.ProgressEvent) {
							msg := cleanupProgressMsg(event)
							if event.Kind == "output" {
								select {
								case m.events <- msg:
								default:
								}
							} else {
								select {
								case m.events <- msg:
								case <-m.ctx.Done():
								}
							}
						})
						p("Rescanning after cleanup")
						scanctx, c := context.WithTimeout(m.ctx, 6*time.Minute)
						defer c()
						select {
						case m.events <- phaseMsg{status: "Rescanning after cleanup", cancel: c}:
						case <-m.ctx.Done():
						}
						s := e.ScanProgress(scanctx, p)
						hs, err := e.History.Load()
						return cleanDone{rs, s, hs, err}
					})
				}
			default:
				if len(k) == 1 && len(m.typed) < 6 {
					m.typed += k
				}
			}
			return m, nil
		}
		switch k {
		case "q", "Q":
			return m, tea.Quit
		case "esc":
			if m.page == "details" {
				m.page = "candidates"
			} else {
				m.page = "dashboard"
			}
			m.offset = 0
			m.cursor = 0
			return m, nil
		case "?":
			m.page = "help"
			m.offset = 0
			return m, nil
		case "h", "H":
			m.page = "history"
			m.offset = 0
			return m, nil
		case "w", "W":
			m.page = "warnings"
			m.offset = 0
			return m, nil
		case "f", "F", "/":
			m.filterDraft = m.settings.Filter
			m.filterCursor = 0
			m.filterEditing = false
			m.filterReturn = m.page
			if m.filterReturn == "details" {
				m.filterReturn = "candidates"
			}
			if m.filterReturn != "candidates" && m.filterReturn != "settings" {
				m.filterReturn = "dashboard"
			}
			m.page = "filters"
			m.offset = 0
			return m, nil
		case "s", "S":
			m.page = "settings"
			m.offset = 0
			return m, nil
		case "r", "R":
			m.page = "dashboard"
			return m, m.scan()
		}
		if m.page == "history" && (k == "e" || k == "E") {
			rs, path := m.history, strings.TrimSuffix(m.engine.History.Path, "history.json")+"history-export.json"
			return m, m.start("Exporting history", func(context.Context, func(string)) tea.Msg {
				return savedMsg{storage.AtomicJSON(path, rs), "Exported to " + path}
			})
		}
		if k == "up" || k == "k" {
			if m.page == "dashboard" || m.page == "candidates" {
				m.cursor = max(0, m.cursor-1)
			} else {
				m.offset = max(0, m.offset-1)
			}
			return m, nil
		}
		if k == "down" || k == "j" {
			switch m.page {
			case "dashboard":
				m.cursor = min(3, m.cursor+1)
			case "candidates":
				m.cursor = min(max(0, len(m.indices())-1), m.cursor+1)
			default:
				m.offset++
			}
			return m, nil
		}
		if k == "pgdown" {
			m.offset += max(1, m.height-8)
			return m, nil
		}
		if k == "pgup" {
			m.offset = max(0, m.offset-max(1, m.height-8))
			return m, nil
		}
		if k == "enter" {
			switch m.page {
			case "dashboard":
				m.category = m.categories()[m.cursor]
				m.page = "candidates"
				m.cursor = 0
			case "candidates":
				if len(m.indices()) > 0 {
					m.page = "details"
					m.offset = 0
				}
			case "review":
				m.page = "confirm"
				m.typed = ""
			case "results":
				m.page = "dashboard"
				m.cursor = 0
				m.offset = 0
			}
			return m, nil
		}
		if (k == "v" || k == "V") && (m.page == "details" || m.page == "candidates") {
			ids := m.indices()
			if m.cursor < len(ids) {
				c := m.snapshot.Candidates[ids[m.cursor]]
				if c.ReviewRequired && !c.Protected {
					m.reviewID = c.ID
					m.typed = ""
					m.offset = 0
					m.page = "support-review"
				} else {
					m.status = "Review unavailable: " + c.ProtectionReason
				}
			}
			return m, nil
		}
		if (m.page == "dashboard" || m.page == "candidates" || m.page == "details") && (k == "space" || k == " ") {
			if m.page == "dashboard" {
				cat := m.categories()[m.cursor]
				all := true
				for _, c := range m.snapshot.Candidates {
					if c.Provider == cat && m.bulkEligible(c) && !c.Selected {
						all = false
					}
				}
				for i := range m.snapshot.Candidates {
					c := &m.snapshot.Candidates[i]
					if c.Provider == cat && m.bulkEligible(*c) {
						c.Selected = !all
					}
				}
			} else {
				ids := m.indices()
				if m.cursor < len(ids) {
					c := &m.snapshot.Candidates[ids[m.cursor]]
					if c.Eligible() {
						c.Selected = !c.Selected
					} else {
						if c.ReviewRequired && !c.Reviewed && !c.Protected {
							m.status = "Individual review required: V opens resource review"
						} else {
							m.status = "Protected: " + c.ProtectionReason
						}
					}
				}
			}
			return m, nil
		}
		if (k == "a" || k == "A") && (m.page == "dashboard" || m.page == "candidates") {
			for i := range m.snapshot.Candidates {
				c := &m.snapshot.Candidates[i]
				if m.bulkEligible(*c) && (m.page == "dashboard" || c.Provider == m.category) {
					c.Selected = true
				}
			}
			m.status = "Eligible items selected; C reviews exact scope"
			return m, nil
		}
		if (k == "p" || k == "P") && (m.page == "candidates" || m.page == "details") {
			ids := m.indices()
			if m.cursor < len(ids) {
				c := &m.snapshot.Candidates[ids[m.cursor]]
				found := false
				for i, id := range m.settings.Pinned {
					if id == c.ID {
						m.settings.Pinned = append(m.settings.Pinned[:i], m.settings.Pinned[i+1:]...)
						found = true
						break
					}
				}
				if !found {
					m.settings.Pinned = append(m.settings.Pinned, c.ID)
					c.Protected = true
					c.Selected = false
					c.ProtectionReason = "Pinned in settings"
				}
				s, path := m.settings, m.settingsPath
				return m, m.start("Saving pin; rescan to refresh protection", func(context.Context, func(string)) tea.Msg {
					return savedMsg{config.Save(path, s), "Pin updated; R rescans protection state"}
				})
			}
			return m, nil
		}
		if (k == "o" || k == "O" || k == "t" || k == "T") && m.page == "candidates" {
			m.settings.Filter.Sort = "size"
			if k == "t" || k == "T" {
				m.settings.Filter.Sort = "modified"
			}
			m.cursor = 0
			return m, m.saveSettings("Sort preference saved")
		}
		if (k == "c" || k == "C") && (m.page == "dashboard" || m.page == "candidates" || m.page == "details") {
			cs, e, age := m.selected(), m.engine, m.settings.CacheAgeDays
			return m, m.start("Validating cleanup plan", func(ctx context.Context, p func(string)) tea.Msg {
				if _, err := e.History.Load(); err != nil {
					return planDone{err: err}
				}
				ops, err := e.Plan(ctx, cs, age)
				return planDone{ops, err}
			})
		}
	}
	return m, nil
}

func (m *Model) appendLive(text string) {
	m.liveOutput += strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if len(m.liveOutput) > 65536 {
		m.liveOutput = m.liveOutput[len(m.liveOutput)-65536:]
	}
}
