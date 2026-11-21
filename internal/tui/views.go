package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

var accent = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
var warn = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
var focus = lipgloss.NewStyle().Foreground(lipgloss.Color("121")).Bold(true)

func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (!unicode.IsControl(r) && r != 0x7f) {
			return r
		}
		return -1
	}, ansi.Strip(s))
}
func (m Model) View() tea.View {
	if m.width < 32 || m.height < 14 {
		v := tea.NewView(ansi.Truncate("Resize terminal to at least 32×14. Q quits.", max(1, m.width), ""))
		v.AltScreen = true
		return v
	}
	width := max(1, m.width-4)
	var body string
	switch m.page {
	case "dashboard":
		body = m.dashboard(width)
	case "candidates":
		body = m.candidates(width)
	case "details":
		ids := m.indices()
		if m.cursor < len(ids) {
			c := m.snapshot.Candidates[ids[m.cursor]]
			body = fmt.Sprintf("%s\n\n%s / %s\n%s\n\nLocation: %s\nMeasured/estimated size: %s\nRisk: %s\nModified: %s\n\n%s\n\nProtection/review: %s\n\nSpace selects · V reviews resource · P pins · Esc back", safe(c.Name), c.Provider, c.Category, c.ID, safe(c.Path), storage.Bytes(c.EstimatedBytes), c.Risk, modifiedLabel(c.LastModified), safe(c.Description), candidateStatus(c))
		}
	case "support-review":
		body = "INDIVIDUAL RESOURCE REVIEW\n\n"
		for _, c := range m.snapshot.Candidates {
			if c.ID == m.reviewID {
				body += safe(c.Path) + "\n\n" + safe(c.Description) + "\n\nSize: " + storage.Bytes(c.EstimatedBytes) + "\nModified: " + modifiedLabel(c.LastModified) + "\n\n"
			}
		}
		body += "Device support may be needed for historical crash symbolication. Simulator deletion permanently loses installed apps, test databases and settings; runtime removal requires a new download. Preserve or pin resources you need.\n\nType REVIEW and press Enter to acknowledge this resource only. This does not delete or select it. A separate plan review and DELETE confirmation are still required. Fresh safety checks run before execution.\n\n> " + m.typed + "\n\nEsc cancels."
	case "review":
		body = "CLEANUP REVIEW\n\n"
		for i, op := range m.ops {
			body += fmt.Sprintf("%d. %s · %s · risk %s\nScope: %s\n%s\n\n", i+1, safe(op.Candidate.Name), storage.Bytes(op.Candidate.EstimatedBytes), op.Candidate.Risk, safe(op.Scope), safe(op.Warning))
		}
		body += "Estimated selected total: " + storage.Bytes(storage.Estimate(m.selected())) + "\nEstimates are not guaranteed savings. Read the full scope above.\n\nEnter continues to permanent deletion confirmation · Esc cancels"
	case "confirm":
		body = "PERMANENT DELETION\n\nThe reviewed operations cannot be undone.\nType DELETE and press Enter to execute the reviewed plan.\n\n> " + m.typed + "\n\nEsc returns to review"
	case "progress":
		body = m.progressView(width)
	case "results":
		success, failed := 0, 0
		var estimated, reported uint64
		known := 0
		var observed int64
		for _, r := range m.results {
			if r.Success {
				success++
			} else {
				failed++
			}
			estimated += r.EstimatedBytes
			if r.ProviderReportedBytes != nil {
				reported += *r.ProviderReportedBytes
				known++
			}
			if r.MeasurementValid {
				observed += r.DiskDeltaBytes
			}
		}
		body = fmt.Sprintf("CLEANUP RESULTS\n\nSucceeded %d · Failed %d · Not started %d\nEstimated: %s\nProvider reported (known %d/%d): %s\nObserved disk change: %s\n\n", success, failed, max(0, len(m.ops)-len(m.results)), storage.Bytes(estimated), known, len(m.results), storage.Bytes(reported), storage.Signed(observed))
		if len(m.results) == 0 {
			body += "No operations executed (canceled).\n"
		}
		for _, r := range m.results {
			body += recordText(r)
		}
		body += "Current session observed change: " + storage.Signed(m.session) + "\nCumulative observed change: " + storage.Signed(storage.Lifetime(m.history)) + "\n\nDocker/APFS may delay physical reclamation. Concurrent disk activity affects observations.\nEnter returns to dashboard."
	case "history":
		body = "CLEANUP HISTORY\n\nCumulative observed space change: " + storage.Signed(storage.Lifetime(m.history)) + "\nThis is a historical sum, not currently available space.\n\n"
		if len(m.history) == 0 {
			body += "No cleanup sessions recorded.\n"
		}
		totals := map[string]int64{}
		for _, r := range m.history {
			if r.MeasurementValid {
				totals[r.Provider] += r.DiskDeltaBytes
			}
		}
		for _, name := range m.categories() {
			body += name + ": " + storage.Signed(totals[name]) + " observed\n"
		}
		body += "\n"
		for i := len(m.history) - 1; i >= 0; i-- {
			body += recordText(m.history[i])
		}
		body += "E exports history JSON to the application configuration directory."
	case "settings":
		body = m.settingsView()
	case "filters":
		body = m.filtersView()
	case "warnings":
		body = "SCAN WARNINGS\n\n" + safe(strings.Join(m.snapshot.Errors, "\n\n")) + "\n\nEsc returns to dashboard."
	case "help":
		body = "HELP\n\n↑/↓ or J/K  Navigate or scroll\nEnter       Category / details / review continuation\nSpace       Select candidate or eligible category\nA           Select visible eligible items within bulk risk ceiling\nC           Build and review cleanup plan\nR           Rescan (read-only)\nW           Full scan warnings\nH           History; E exports JSON\nF / /       Persistent cleanup filters\nS           Cleanup and live-output preferences\nV           Review a device-support/simulator resource\nP           Pin/unpin candidate\nO / T       Sort candidates by size / modification time\nEsc         Back or request cancellation\nPgUp/PgDn   Scroll live output; End follows latest\nQ           Quit when idle\n\nCleanup always requires review followed by typing DELETE.\nVolumes and archives remain protected. Device support and simulators require individual review. Simulator bundles are never removed directly.\nClose Xcode and xcodebuild before filesystem/simulator cleanup.\nZero estimate for Docker images/containers means unknown unique savings, not zero usage.\nRead provider errors on the dashboard; incomplete scans are not complete inventories."
	}
	body = ansi.Wrap(body, width, "")
	lines := strings.Split(body, "\n")
	available := max(1, m.height-7)
	start := min(m.offset, max(0, len(lines)-available))
	if m.page == "filters" || m.page == "settings" {
		for i, line := range lines {
			if (!m.filterEditing && strings.Contains(ansi.Strip(line), "› ")) || (m.page == "filters" && m.filterEditing && strings.HasPrefix(ansi.Strip(line), "Edit:")) {
				start = max(0, min(i-available+3, max(0, len(lines)-available)))
				break
			}
		}
	}
	end := min(len(lines), start+available)
	header := accent.Render(ansi.Truncate("devclean  /  Developer Storage Cleaner", width, "…")) + "\n" + muted.Render(strings.Repeat("─", width)) + "\n"
	status := safe(m.status)
	if m.busy {
		status = m.spinner.View() + " " + status + " · " + time.Since(m.started).Round(time.Second).String()
	}
	footer := "\n" + muted.Render(strings.Repeat("─", width)) + "\n" + ansi.Truncate(status, width, "…") + "\n" + muted.Render(ansi.Truncate("Enter details · Space select · C clean · F filters · S settings · R rescan · ? help · Q quit", width, "…"))
	rendered := header + strings.Join(lines[start:end], "\n") + footer
	v := tea.NewView(lipgloss.NewStyle().Padding(0, min(2, max(0, (m.width-1)/2))).Render(rendered))
	v.AltScreen = true
	return v
}
func (m Model) dashboard(width int) string {
	d := m.snapshot.Disk
	filled := min(width, int(float64(max(1, width-14))*d.Percent()/100))
	bar := accent.Render(strings.Repeat("━", filled)) + muted.Render(strings.Repeat("─", max(0, width-14-filled)))
	lastScan := "pending"
	if !m.snapshot.At.IsZero() {
		lastScan = m.snapshot.At.Format("Jan 02 15:04:05")
	}
	b := fmt.Sprintf("DISK OVERVIEW\nTotal %s · Used %s · Free %s\n%s  %.1f%% used\nEstimated reclaimable: %s\nSession observed change: %s\nCumulative observed recovered: %s\nLast scan: %s\n\nCLEANUP CATEGORIES\n", storage.Bytes(d.Total), storage.Bytes(d.Used), storage.Bytes(d.Available), bar, d.Percent(), storage.Bytes(storage.Estimate(m.snapshot.Candidates)), storage.Signed(m.session), storage.Signed(storage.Lifetime(m.history)), lastScan)
	for i, name := range m.categories() {
		var cs []storage.Candidate
		count, selected := 0, 0
		for _, c := range m.snapshot.Candidates {
			if c.Provider == name && m.visible(c) {
				cs = append(cs, c)
				if c.Eligible() {
					count++
					if c.Selected {
						selected++
					}
				}
			}
		}
		line := fmt.Sprintf("  [%d/%d] %-17s %s reclaimable", selected, count, name, storage.Bytes(storage.Estimate(cs)))
		if i == m.cursor {
			line = focus.Render("›" + line[1:])
		}
		b += line + "\n"
	}
	b += "\nFilters: " + safe(m.settings.Filter.Summary()) + " (F)\nSelected estimate: " + storage.Bytes(storage.Estimate(m.selected())) + "\n"
	if len(m.snapshot.Errors) > 0 {
		b += warn.Render(fmt.Sprintf("\n%d scan warnings · W opens details", len(m.snapshot.Errors)))
	}
	return b
}
func (m Model) candidates(width int) string {
	ids := m.indices()
	b := m.category + " / CANDIDATES\n" + safe(m.settings.Filter.Summary()) + " · F edits filters\nSpace selects · Enter details · V review · P pin · O size · T date\n\n"
	if len(ids) == 0 {
		return b + "No candidates available. Inspect scan warnings on the dashboard."
	}
	// Keep the focused row visible without requiring a second scrolling mode.
	rows := max(1, m.height-14)
	start := max(0, m.cursor-rows+1)
	for j := start; j < min(len(ids), start+rows); j++ {
		c := m.snapshot.Candidates[ids[j]]
		mark := " "
		if c.Protected {
			mark = "−"
		} else if c.ReviewRequired && !c.Reviewed {
			mark = "?"
		} else if c.Selected {
			mark = "x"
		}
		line := fmt.Sprintf("[%s] %s · %s · %s", mark, safe(c.Name), storage.Bytes(c.EstimatedBytes), c.Category)
		line = ansi.Truncate(line, width-1, "…")
		if j == m.cursor {
			line = focus.Render("›" + line)
		} else {
			line = " " + line
		}
		b += line + "\n"
	}
	b += fmt.Sprintf("\n%d/%d · ? review required · − protected · Selected estimate %s", m.cursor+1, len(ids), storage.Bytes(storage.Estimate(m.selected())))
	return b
}
func recordText(r storage.Record) string {
	provider := "not reported"
	if r.ProviderReportedBytes != nil {
		provider = storage.Bytes(*r.ProviderReportedBytes)
	}
	observed := "unavailable"
	if r.MeasurementValid {
		observed = storage.Signed(r.DiskDeltaBytes)
	}
	usage := "not measured"
	if r.ProviderUsageBeforeBytes != nil {
		usage = storage.Bytes(*r.ProviderUsageBeforeBytes) + " / unavailable"
		if r.ProviderUsageAfterBytes != nil {
			usage = storage.Bytes(*r.ProviderUsageBeforeBytes) + " / " + storage.Bytes(*r.ProviderUsageAfterBytes)
		}
	}
	return fmt.Sprintf("%s · %s · success=%t\n%s\nEstimated: %s · Provider reported: %s\nObserved disk change: %s · Available before %s / after %s\nProvider usage before/after: %s\n%s\n%s\n%s\n\n", r.Timestamp.Format(time.RFC3339), r.Provider, r.Success, safe(r.Operation), storage.Bytes(r.EstimatedBytes), provider, observed, storage.Bytes(r.AvailableBefore), storage.Bytes(r.AvailableAfter), usage, safe(r.PlanWarning), safe(strings.Join(r.Errors, "\n")), safe(r.Output)+"\nCommand transcript (bounded):\n"+safe(r.CommandOutput))
}

func candidateStatus(c storage.Candidate) string {
	if c.Protected {
		return safe(c.ProtectionReason)
	}
	if c.ReviewRequired {
		if c.Reviewed {
			return "Individual review acknowledged for this scan; fresh resource checks still required"
		}
		return "Individual review required; V opens review"
	}
	return "Eligible"
}

func (m Model) progressView(width int) string {
	elapsed := "pending"
	if !m.operationStarted.IsZero() {
		elapsed = time.Since(m.operationStarted).Round(time.Second).String()
	}
	b := fmt.Sprintf("CLEANUP IN PROGRESS\nFinished %d/%d · Successful %d · Failed %d\n%s\nResource: %s · elapsed %s\nCommand: %s\n", m.completed, m.total, m.completed-m.failed, m.failed, safe(m.status), safe(m.operationName), elapsed, ansi.Truncate(safe(strings.TrimSpace(m.lastCommand)), max(1, width-9), "…"))
	if len(m.completedOperations) > 0 {
		last := m.completedOperations[len(m.completedOperations)-1]
		b += "Last completed: " + ansi.Truncate(safe(last), max(1, width-16), "…") + "\n"
	} else {
		b += "No completed operations yet.\n"
	}
	b += "Esc cancels · Tab output/completed · PgUp/PgDn scroll · End follows latest\n"
	if m.progressTab == 1 {
		lines := strings.Split(ansi.Wrap(safe(strings.Join(m.completedOperations, "\n")), width, ""), "\n")
		rows := max(1, m.height-17)
		end := len(lines) - min(m.outputOffset, max(0, len(lines)-rows))
		start := max(0, end-rows)
		return b + "\nCOMPLETED OPERATIONS\n" + strings.Join(lines[start:end], "\n")
	}
	if !m.settings.ShowCommandOutput {
		return b + "\nLive output display disabled in preferences. The bounded transcript is saved with each record."
	}
	lines := strings.Split(ansi.Wrap(safe(m.liveOutput), width, ""), "\n")
	if len(lines) > m.settings.OutputLines {
		lines = lines[len(lines)-m.settings.OutputLines:]
	}
	rows := max(1, m.height-17)
	offset := min(m.outputOffset, max(0, len(lines)-rows))
	end := len(lines) - offset
	start := max(0, end-rows)
	b += "\nLIVE COMMAND OUTPUT\n" + strings.Join(lines[start:end], "\n")
	return b
}

func modifiedLabel(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format(time.RFC3339)
}
