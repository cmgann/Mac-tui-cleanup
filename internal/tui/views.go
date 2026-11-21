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
			body = fmt.Sprintf("%s\n\n%s / %s\n%s\n\nLocation: %s\nMeasured/estimated size: %s\nRisk: %s\nModified: %s\n\n%s\n\nProtection/review: %s\n\nSpace selects · V reviews device support · P pins · Esc back", safe(c.Name), c.Provider, c.Category, c.ID, safe(c.Path), storage.Bytes(c.EstimatedBytes), c.Risk, c.LastModified.Format(time.RFC3339), safe(c.Description), candidateStatus(c))
		}
	case "support-review":
		body = "DEVICE SUPPORT / INDIVIDUAL REVIEW\n\n"
		for _, c := range m.snapshot.Candidates {
			if c.ID == m.reviewID {
				body += safe(c.Path) + "\n\n" + safe(c.Description) + "\n\nSize: " + storage.Bytes(c.EstimatedBytes) + "\nModified: " + c.LastModified.Format(time.RFC3339) + "\n\n"
			}
		}
		body += "Deleting support files can prevent symbolication of historical crash reports. Keep release-related versions or pin this folder if unsure. Recent entries require this same explicit review.\n\nType REVIEW and press Enter to acknowledge this folder only. This does not delete or select it. A separate cleanup review and DELETE confirmation are still required. Device state will be checked again.\n\n> " + m.typed + "\n\nEsc cancels."
	case "review":
		body = "CLEANUP REVIEW\n\n"
		for i, op := range m.ops {
			body += fmt.Sprintf("%d. %s · %s · risk %s\nScope: %s\n%s\n\n", i+1, safe(op.Candidate.Name), storage.Bytes(op.Candidate.EstimatedBytes), op.Candidate.Risk, safe(op.Scope), safe(op.Warning))
		}
		body += "Estimated selected total: " + storage.Bytes(storage.Estimate(m.selected())) + "\nEstimates are not guaranteed savings. Read the full scope above.\n\nEnter continues to permanent deletion confirmation · Esc cancels"
	case "confirm":
		body = "PERMANENT DELETION\n\nThe reviewed operations cannot be undone.\nType DELETE and press Enter to execute the reviewed plan.\n\n> " + m.typed + "\n\nEsc returns to review"
	case "progress":
		body = "CLEANUP IN PROGRESS\n\n" + safe(m.status) + "\n\nEsc requests cancellation. Completed work cannot be undone.\nPartial results will be saved and shown."
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
		age := fmt.Sprintf("%d days", m.settings.CacheAgeDays)
		if m.settings.CacheAgeDays == 0 {
			age = "ALL reclaimable cache (no age restriction)"
		}
		body = "SETTINGS\n\nBuildKit cache age policy: " + age + "\nEnter cycles 30 days / 7 days / all.\n\nPins: " + fmt.Sprint(len(m.settings.Pinned)) + "\nP on a candidate pins/unpins it; rescan after unpinning.\n\nSettings: " + safe(m.settingsPath) + "\nLocal JSON only. No telemetry or background deletion."
	case "warnings":
		body = "SCAN WARNINGS\n\n" + safe(strings.Join(m.snapshot.Errors, "\n\n")) + "\n\nEsc returns to dashboard."
	case "help":
		body = "HELP\n\n↑/↓ or J/K  Navigate or scroll\nEnter       Category / details / review continuation\nSpace       Select candidate or eligible category\nA           Select eligible items in current view\nC           Build and review cleanup plan\nR           Rescan (read-only)\nW           Full scan warnings\nH           History; E exports JSON\nS           Settings and BuildKit age policy\nV           Review one device-support folder\nP           Pin/unpin candidate\nO / T       Sort candidates by size / modification time\nEsc         Back or request cancellation\nQ           Quit when idle\n\nCleanup always requires review followed by typing DELETE.\nVolumes, archives and simulator bundles remain protected. Device support requires V review and individual selection.\nClose Xcode and xcodebuild before DerivedData cleanup.\nZero estimate for Docker images/containers means unknown unique savings, not zero usage.\nRead provider errors on the dashboard; incomplete scans are not complete inventories."
	}
	body = ansi.Wrap(body, width, "")
	lines := strings.Split(body, "\n")
	available := max(1, m.height-7)
	start := min(m.offset, max(0, len(lines)-available))
	end := min(len(lines), start+available)
	header := accent.Render(ansi.Truncate("devclean  /  Developer Storage Cleaner", width, "…")) + "\n" + muted.Render(strings.Repeat("─", width)) + "\n"
	status := safe(m.status)
	if m.busy {
		status = m.spinner.View() + " " + status + " · " + time.Since(m.started).Round(time.Second).String()
	}
	footer := "\n" + muted.Render(strings.Repeat("─", width)) + "\n" + ansi.Truncate(status, width, "…") + "\n" + muted.Render(ansi.Truncate("Enter details · Space select · C clean · R rescan · H history · ? help · Q quit", width, "…"))
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
			if c.Provider == name {
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
	b += "\nSelected estimate: " + storage.Bytes(storage.Estimate(m.selected())) + "\n"
	if len(m.snapshot.Errors) > 0 {
		b += warn.Render(fmt.Sprintf("\n%d scan warnings · W opens details", len(m.snapshot.Errors)))
	}
	return b
}
func (m Model) candidates(width int) string {
	ids := m.indices()
	b := m.category + " / CANDIDATES\nSpace selects · Enter details · V review · P pin · O size · T date\n\n"
	if len(ids) == 0 {
		return b + "No candidates available. Inspect scan warnings on the dashboard."
	}
	// Keep the focused row visible without requiring a second scrolling mode.
	rows := max(1, m.height-12)
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
	return fmt.Sprintf("%s · %s · success=%t\n%s\nEstimated: %s · Provider reported: %s\nObserved disk change: %s · Available before %s / after %s\nProvider usage before/after: %s\n%s\n%s\n%s\n\n", r.Timestamp.Format(time.RFC3339), r.Provider, r.Success, safe(r.Operation), storage.Bytes(r.EstimatedBytes), provider, observed, storage.Bytes(r.AvailableBefore), storage.Bytes(r.AvailableAfter), usage, safe(r.PlanWarning), safe(strings.Join(r.Errors, "\n")), safe(r.Output))
}

func candidateStatus(c storage.Candidate) string {
	if c.Protected {
		return safe(c.ProtectionReason)
	}
	if c.ReviewRequired {
		if c.Reviewed {
			return "Individual review acknowledged for this scan; fresh device checks still required"
		}
		return "Individual review required; V opens review"
	}
	return "Eligible"
}
