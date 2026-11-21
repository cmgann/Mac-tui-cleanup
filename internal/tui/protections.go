package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cmgann/Mac-tui-cleanup/internal/config"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

type protectedSavedMsg struct {
	settings   config.Settings
	candidates []storage.Candidate
	err        error
}

func (m *Model) protectionKey(k string) (bool, tea.Cmd) {
	if m.page != "protections" {
		return false, nil
	}
	if m.protectedEditing {
		switch k {
		case "esc":
			m.protectedEditing = false
			m.typed = ""
		case "enter", "ctrl+s":
			value := strings.TrimSpace(m.typed)
			if !(filepath.IsAbs(value) || value == "~" || strings.HasPrefix(value, "~/")) {
				m.status = "Use an absolute path or ~/path; paths are literal, not patterns"
				return true, nil
			}
			m.protectedDraft = append(m.protectedDraft, value)
			m.protectedEditing = false
			m.typed = ""
			if k == "ctrl+s" {
				return true, m.saveProtections()
			}
		case "backspace":
			r := []rune(m.typed)
			if len(r) > 0 {
				m.typed = string(r[:len(r)-1])
			}
		default:
			if k == "space" {
				k = " "
			}
			if len([]rune(k)) == 1 && len([]rune(m.typed)) < 4096 {
				m.typed += k
			}
		}
		return true, nil
	}
	switch k {
	case "esc":
		m.page = "settings"
		m.offset = 0
	case "up", "k":
		m.protectedCursor = max(0, m.protectedCursor-1)
	case "down", "j":
		m.protectedCursor = min(max(0, len(m.protectedDraft)-1), m.protectedCursor+1)
	case "a", "A":
		m.typed = ""
		m.protectedEditing = true
	case "d", "D":
		if m.protectedCursor < len(m.protectedDraft) {
			m.protectedDraft = append(m.protectedDraft[:m.protectedCursor], m.protectedDraft[m.protectedCursor+1:]...)
			m.protectedCursor = max(0, min(m.protectedCursor, len(m.protectedDraft)-1))
		}
	case "ctrl+s":
		return true, m.saveProtections()
	}
	return true, nil
}
func (m *Model) saveProtections() tea.Cmd {
	settings := m.settings
	paths := append([]string(nil), m.protectedDraft...)
	cs := append([]storage.Candidate(nil), m.snapshot.Candidates...)
	home, configPath := m.engine.Home, m.settingsPath
	return m.start("Validating protected paths", func(context.Context, func(string)) tea.Msg {
		var normalized []string
		seen := map[string]bool{}
		for _, path := range paths {
			p, e := storage.CanonicalPath(path, home)
			if e != nil {
				return protectedSavedMsg{err: e}
			}
			if !seen[p] {
				normalized = append(normalized, p)
				seen[p] = true
			}
		}
		settings.ProtectedPaths = normalized
		if e := config.Save(configPath, settings); e != nil {
			return protectedSavedMsg{err: e}
		}
		settings.Policy(home).Apply(cs, time.Now())
		return protectedSavedMsg{settings: settings, candidates: cs}
	})
}
func (m Model) protectionsView() string {
	b := "PROTECTED PATHS\n\nDraft changes apply only after Ctrl+S. Protected ancestors or descendants block an entire tool action when partial exclusion is unavailable.\n\n"
	for i, p := range m.protectedDraft {
		if i == m.protectedCursor {
			b += focus.Render("› "+safe(p)) + "\n"
		} else {
			b += "  " + safe(p) + "\n"
		}
	}
	if len(m.protectedDraft) == 0 {
		b += "No custom protected paths. Built-in protections and pins still apply.\n"
	}
	if m.protectedEditing {
		b += "\nPath: " + safe(m.typed) + "\nEnter accepts path · Esc cancels entry\n"
	}
	return b + fmt.Sprintf("\n%d protected paths · A add · D remove draft entry · Ctrl+S save · Esc cancel\nUse literal absolute paths or ~/path. Removing a protection requires a rescan before previously protected candidates become selectable.", len(m.protectedDraft))
}
