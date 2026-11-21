// Package schedule installs only explicitly requested native read-only scan jobs.
package schedule

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
)

const label = "io.github.cmgann.devclean.scan"
const marker = "Managed by devclean: READ-ONLY scans only"

type Manager struct {
	OS, Home, ConfigDir, Executable, Path, ConfigHome string
	UID                                               int
	Run                                               runner.Runner
}
type Status struct {
	Configured, Enabled, Loaded bool
	Files                       []string
	Description                 string
}

func xmlText(s string) string { var b bytes.Buffer; xml.EscapeText(&b, []byte(s)); return b.String() }
func unitText(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(s) + `"`
}
func (m Manager) paths() []string {
	if m.OS == "darwin" {
		return []string{filepath.Join(m.Home, "Library/LaunchAgents", label+".plist")}
	}
	base := m.ConfigHome
	if base == "" {
		base = filepath.Join(m.Home, ".config")
	}
	return []string{filepath.Join(base, "systemd/user/devclean-scan.service"), filepath.Join(base, "systemd/user/devclean-scan.timer")}
}
func (m Manager) definitions(mode string, hour int) (map[string][]byte, error) {
	if mode != "daily" && mode != "weekly" {
		return nil, errors.New("schedule must be daily, weekly, off or status")
	}
	if hour < 0 || hour > 23 {
		return nil, errors.New("schedule hour must be between 0 and 23")
	}
	for _, p := range []string{m.Home, m.ConfigDir, m.Executable} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\r\n\x00") {
			return nil, errors.New("scheduler requires absolute, single-line paths")
		}
	}
	if strings.ContainsAny(m.Path, "\r\n\x00") {
		return nil, errors.New("invalid scheduler PATH")
	}
	paths := m.paths()
	defs := map[string][]byte{}
	if m.OS == "darwin" {
		calendar := fmt.Sprintf("<key>Hour</key><integer>%d</integer><key>Minute</key><integer>0</integer>", hour)
		if mode == "weekly" {
			calendar += "<key>Weekday</key><integer>1</integer>"
		}
		text := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- ` + marker + ` -->
<plist version="1.0"><dict><key>Label</key><string>` + label + `</string>
<key>ProgramArguments</key><array><string>` + xmlText(m.Executable) + `</string><string>--scheduled-scan</string></array>
<key>StartCalendarInterval</key><dict>` + calendar + `</dict><key>RunAtLoad</key><false/>
<key>ProcessType</key><string>Background</string><key>Umask</key><integer>63</integer>
<key>EnvironmentVariables</key><dict><key>HOME</key><string>` + xmlText(m.Home) + `</string><key>PATH</key><string>` + xmlText(m.Path) + `</string></dict>
<key>StandardOutPath</key><string>` + xmlText(filepath.Join(m.ConfigDir, "scheduled-scan.log")) + `</string>
<key>StandardErrorPath</key><string>` + xmlText(filepath.Join(m.ConfigDir, "scheduled-scan.log")) + `</string></dict></plist>
`
		defs[paths[0]] = []byte(text)
	} else if m.OS == "linux" {
		calendar := fmt.Sprintf("*-*-* %02d:00:00", hour)
		if mode == "weekly" {
			calendar = "Mon " + calendar
		}
		configHome := m.ConfigHome
		if configHome == "" {
			configHome = filepath.Join(m.Home, ".config")
		}
		if !filepath.IsAbs(configHome) || strings.ContainsAny(configHome, "\r\n\x00") {
			return nil, errors.New("invalid scheduler configuration directory")
		}
		service := "# " + marker + "\n[Unit]\nDescription=devclean read-only developer storage scan\n[Service]\nType=oneshot\nExecStart=:" + unitText(m.Executable) + " --scheduled-scan\nEnvironment=" + unitText("HOME="+m.Home) + " " + unitText("PATH="+m.Path) + " " + unitText("XDG_CONFIG_HOME="+configHome) + "\nUMask=0077\nNice=10\nNoNewPrivileges=true\nTimeoutStartSec=7min\n"
		timer := "# " + marker + "\n[Unit]\nDescription=devclean scheduled read-only scan\n[Timer]\nOnCalendar=" + calendar + "\nPersistent=true\nUnit=devclean-scan.service\n[Install]\nWantedBy=timers.target\n"
		defs[paths[0]] = []byte(service)
		defs[paths[1]] = []byte(timer)
	} else {
		return nil, errors.New("scheduled scans support macOS launchd and Linux systemd user sessions")
	}
	return defs, nil
}
func readOwned(path string) ([]byte, error) {
	info, e := os.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("scheduler file is a symlink or special entry")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if !bytes.Contains(b, []byte(marker)) {
		return nil, fmt.Errorf("refusing to replace unmanaged scheduler file: %s", path)
	}
	return b, nil
}
func write(path string, b []byte) error { return storage.AtomicWrite(path, b) }
func (m Manager) target() string        { return "gui/" + strconv.Itoa(m.UID) + "/" + label }
func (m Manager) Status(ctx context.Context) (Status, error) {
	s := Status{}
	for _, path := range m.paths() {
		b, e := readOwned(path)
		if e != nil {
			return s, e
		}
		if b != nil {
			s.Files = append(s.Files, path)
		}
	}
	if len(s.Files) == 0 {
		s.Description = "No read-only scan schedule installed"
		return s, nil
	}
	s.Configured = true
	s.Description = "Managed read-only schedule files present; native scheduler status follows"
	if m.OS == "darwin" {
		_, e := m.Run.Run(ctx, "launchctl", "print", m.target())
		s.Loaded = e == nil
		if e != nil {
			s.Description += "; launchd job is not loaded or this GUI session is unavailable"
		}
	} else if m.OS == "linux" {
		_, e := m.Run.Run(ctx, "systemctl", "--user", "is-enabled", "devclean-scan.timer")
		s.Loaded = e == nil
		if e != nil {
			s.Description += "; systemd user timer is not enabled or user session is unavailable"
		}
	} else {
		return s, errors.New("unsupported scheduler platform")
	}
	s.Enabled = s.Loaded
	return s, nil
}
func (m Manager) Install(ctx context.Context, mode string, hour int) error {
	defs, e := m.definitions(mode, hour)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, e := os.Stat(m.Executable)
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("scheduled executable must be a regular executable binary")
	}
	unlock, e := storage.FileLock(filepath.Join(m.ConfigDir, "schedule-manager.lock"))
	if e != nil {
		return e
	}
	defer unlock()
	previous := map[string][]byte{}
	for _, path := range m.paths() {
		b, e := readOwned(path)
		if e != nil {
			return e
		}
		previous[path] = b
	}
	rollback := func() {
		for _, path := range m.paths() {
			if b := previous[path]; b != nil {
				_ = write(path, b)
			} else {
				_ = os.Remove(path)
			}
		}
	}
	for _, path := range m.paths() {
		if e = write(path, defs[path]); e != nil {
			rollback()
			return e
		}
	}
	if m.OS == "darwin" {
		logPath := filepath.Join(m.ConfigDir, "scheduled-scan.log")
		if info, e := os.Lstat(logPath); e == nil && !info.Mode().IsRegular() {
			rollback()
			return errors.New("scheduled log path is not a regular file")
		}
		f, e := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			rollback()
			return e
		}
		e = f.Chmod(0600)
		f.Close()
		if e != nil {
			rollback()
			return e
		}
		wasLoaded := false
		if _, e = m.Run.Run(ctx, "launchctl", "print", m.target()); e == nil {
			wasLoaded = true
			if _, e = m.Run.Run(ctx, "launchctl", "bootout", m.target()); e != nil {
				rollback()
				return e
			}
		}
		if _, e = m.Run.Run(ctx, "launchctl", "enable", m.target()); e != nil {
			rollback()
			return e
		}
		if _, e = m.Run.Run(ctx, "launchctl", "bootstrap", "gui/"+strconv.Itoa(m.UID), m.paths()[0]); e != nil {
			rollback()
			if wasLoaded && previous[m.paths()[0]] != nil {
				_, restoreErr := m.Run.Run(ctx, "launchctl", "bootstrap", "gui/"+strconv.Itoa(m.UID), m.paths()[0])
				return errors.Join(e, restoreErr)
			}
			return fmt.Errorf("launchd registration failed; configuration rolled back: %w", e)
		}
	} else {
		if _, e = m.Run.Run(ctx, "systemctl", "--user", "daemon-reload"); e == nil {
			_, e = m.Run.Run(ctx, "systemctl", "--user", "enable", "--now", "devclean-scan.timer")
		}
		if e != nil {
			rollback()
			_, restoreErr := m.Run.Run(ctx, "systemctl", "--user", "daemon-reload")
			return errors.Join(fmt.Errorf("systemd registration failed; configuration rolled back: %w", e), restoreErr)
		}
	}
	return nil
}
func (m Manager) Disable(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	present := false
	for _, path := range m.paths() {
		b, e := readOwned(path)
		if e != nil {
			return e
		}
		present = present || b != nil
	}
	if !present {
		return nil
	}
	unlock, e := storage.FileLock(filepath.Join(m.ConfigDir, "schedule-manager.lock"))
	if e != nil {
		return e
	}
	defer unlock()
	if m.OS == "darwin" {
		if _, e = m.Run.Run(ctx, "launchctl", "disable", m.target()); e != nil {
			return e
		}
		if _, e = m.Run.Run(ctx, "launchctl", "print", m.target()); e == nil {
			if _, e = m.Run.Run(ctx, "launchctl", "bootout", m.target()); e != nil {
				return e
			}
		}
	} else if m.OS == "linux" {
		if _, e = m.Run.Run(ctx, "systemctl", "--user", "disable", "--now", "devclean-scan.timer"); e != nil {
			return e
		}
		if _, e = m.Run.Run(ctx, "systemctl", "--user", "stop", "devclean-scan.service"); e != nil {
			return e
		}
	} else {
		return errors.New("unsupported scheduler platform")
	}
	for _, path := range m.paths() {
		if _, e = readOwned(path); e != nil {
			return e
		}
		if e = os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if m.OS == "linux" {
		_, e = m.Run.Run(ctx, "systemctl", "--user", "daemon-reload")
		return e
	}
	return nil
}
