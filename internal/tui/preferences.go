package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cmgann/Mac-tui-cleanup/internal/config"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

func (m *Model) saveSettings(status string) tea.Cmd {
	s, path := m.settings, m.settingsPath
	return m.start("Saving preferences", func(context.Context, func(string)) tea.Msg { return savedMsg{config.Save(path, s), status} })
}
func (m Model) visible(c storage.Candidate) bool { return m.settings.Filter.Matches(c, time.Now()) }
func (m Model) bulkEligible(c storage.Candidate) bool {
	return c.Eligible() && !c.ReviewRequired && m.visible(c) && config.RiskRank(c.Risk) <= config.RiskRank(m.settings.BulkMaxRisk)
}
func (m *Model) filterChanged() {
	for i := range m.snapshot.Candidates {
		c := &m.snapshot.Candidates[i]
		if !m.visible(*c) {
			c.Selected = false
		}
	}
	m.cursor = 0
	m.offset = 0
}
func cycleRisk(r storage.Risk, allowAll bool) storage.Risk {
	switch r {
	case storage.Low:
		return storage.Medium
	case storage.Medium:
		return storage.High
	case storage.High:
		if allowAll {
			return ""
		}
		return storage.Low
	default:
		return storage.Low
	}
}
func nextNumber(current int, values []int) int {
	for i, v := range values {
		if v == current {
			return values[(i+1)%len(values)]
		}
	}
	return values[0]
}
func (m *Model) preferenceKey(k string) (bool, tea.Cmd) {
	if m.page == "settings" {
		switch k {
		case "up", "k":
			m.settingsCursor = max(0, m.settingsCursor-1)
			return true, nil
		case "down", "j":
			m.settingsCursor = min(3, m.settingsCursor+1)
			return true, nil
		case "enter", "space", " ":
			switch m.settingsCursor {
			case 0:
				m.settings.CacheAgeDays = nextNumber(m.settings.CacheAgeDays, []int{30, 7, 0})
			case 1:
				m.settings.BulkMaxRisk = cycleRisk(m.settings.BulkMaxRisk, false)
			case 2:
				m.settings.OutputLines = nextNumber(m.settings.OutputLines, []int{50, 100, 200})
			case 3:
				m.settings.ShowCommandOutput = !m.settings.ShowCommandOutput
			}
			return true, m.saveSettings("Preferences saved")
		}
	}
	if m.page != "filters" {
		return false, nil
	}
	if m.filterEditing {
		switch k {
		case "esc":
			m.filterEditing = false
			m.typed = ""
		case "enter":
			m.setFilterText()
			m.filterEditing = false
		case "ctrl+s":
			m.setFilterText()
			return true, m.applyFilters()
		case "backspace":
			r := []rune(m.typed)
			if len(r) > 0 {
				m.typed = string(r[:len(r)-1])
			}
		default:
			if k == "space" {
				k = " "
			}
			if len([]rune(k)) == 1 && len([]rune(m.typed)) < 128 {
				m.typed += k
			}
		}
		return true, nil
	}
	switch k {
	case "esc":
		m.page = m.filterReturn
		m.offset = 0
		return true, nil
	case "up", "k":
		m.filterCursor = max(0, m.filterCursor-1)
	case "down", "j":
		m.filterCursor = min(6, m.filterCursor+1)
	case "ctrl+s":
		return true, m.applyFilters()
	case "x", "X":
		m.filterDraft = config.Filter{Sort: "size", Visibility: "all"}
	case "enter", "space", " ":
		switch m.filterCursor {
		case 0:
			m.typed = m.filterDraft.Query
			m.filterEditing = true
		case 1:
			m.typed = m.filterDraft.Category
			m.filterEditing = true
		case 2:
			values := []uint64{0, 100000000, 1000000000, 10000000000}
			current := 0
			for i, v := range values {
				if m.filterDraft.MinBytes == v {
					current = i
				}
			}
			m.filterDraft.MinBytes = values[(current+1)%len(values)]
		case 3:
			m.filterDraft.OlderThanDays = nextNumber(m.filterDraft.OlderThanDays, []int{0, 7, 30, 90})
		case 4:
			m.filterDraft.MaxRisk = cycleRisk(m.filterDraft.MaxRisk, true)
		case 5:
			switch m.filterDraft.Visibility {
			case "all", "":
				m.filterDraft.Visibility = "actionable"
			case "actionable":
				m.filterDraft.Visibility = "eligible"
			default:
				m.filterDraft.Visibility = "all"
			}
		case 6:
			switch m.filterDraft.Sort {
			case "size":
				m.filterDraft.Sort = "name"
			case "name":
				m.filterDraft.Sort = "modified"
			case "modified":
				m.filterDraft.Sort = "risk"
			default:
				m.filterDraft.Sort = "size"
			}
		}
	}
	return true, nil
}
func (m *Model) setFilterText() {
	if m.filterCursor == 0 {
		m.filterDraft.Query = strings.TrimSpace(m.typed)
	} else {
		m.filterDraft.Category = strings.TrimSpace(m.typed)
	}
	m.typed = ""
}
func (m *Model) applyFilters() tea.Cmd {
	m.settings.Filter = m.filterDraft
	m.settings = config.Normalize(m.settings)
	m.filterChanged()
	m.page = m.filterReturn
	return m.saveSettings("Filters saved; hidden candidates deselected")
}
func (m Model) filtersView() string {
	risk := string(m.filterDraft.MaxRisk)
	if risk == "" {
		risk = "all"
	}
	minSize := "any"
	if m.filterDraft.MinBytes > 0 {
		minSize = storage.Bytes(m.filterDraft.MinBytes)
	}
	age := "any (including unknown dates)"
	if m.filterDraft.OlderThanDays > 0 {
		age = fmt.Sprintf("at least %d days (unknown dates hidden)", m.filterDraft.OlderThanDays)
	}
	rows := []string{"Search: " + m.filterDraft.Query, "Category contains: " + m.filterDraft.Category, "Minimum measured/estimated size: " + minSize, "Last modified: " + age, "Maximum cleanup risk: " + risk, "Visibility: " + m.filterDraft.Visibility, "Sort: " + m.filterDraft.Sort}
	b := "CLEANUP FILTERS\n\n"
	for i, row := range rows {
		if i == m.filterCursor {
			b += focus.Render("› "+safe(row)) + "\n"
		} else {
			b += "  " + safe(row) + "\n"
		}
	}
	if m.filterEditing {
		b += "\nEdit: " + safe(m.typed) + "\nEnter accepts text · Esc cancels text edit\n"
	}
	return b + "\n↑/↓ field · Enter edit/cycle · Ctrl+S apply/save · X reset · Esc cancel\nFilters affect the candidate view and bulk selection. Applying filters deselects hidden items. Zero-sized/unknown estimates do not pass a positive size threshold. Pins and individual reviews still apply."
}
func (m Model) settingsView() string {
	age := fmt.Sprintf("%d days", m.settings.CacheAgeDays)
	if m.settings.CacheAgeDays == 0 {
		age = "ALL reclaimable cache"
	}
	rows := []string{"BuildKit age policy: " + age, "Bulk-selection risk ceiling: " + string(m.settings.BulkMaxRisk), fmt.Sprintf("Live output history lines: %d", m.settings.OutputLines), fmt.Sprintf("Show live command output: %t", m.settings.ShowCommandOutput)}
	b := "PREFERENCES\n\n"
	for i, row := range rows {
		if i == m.settingsCursor {
			b += focus.Render("› "+row) + "\n"
		} else {
			b += "  " + row + "\n"
		}
	}
	return b + "\n↑/↓ selects · Enter cycles and saves · F opens filters\n\nHigh-risk items can still be individually selected. Items requiring review are never bulk selected. Live output is bounded; the latest 64 KiB transcript is kept with each cleanup record regardless of display preference.\n\nPins: " + fmt.Sprint(len(m.settings.Pinned)) + "\nSettings: " + safe(m.settingsPath)
}
