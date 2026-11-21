package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cmgann/Mac-tui-cleanup/internal/config"
	"github.com/cmgann/Mac-tui-cleanup/internal/providers"
	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/schedule"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
	"github.com/cmgann/Mac-tui-cleanup/internal/tui"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "devclean:", err)
		os.Exit(1)
	}
}
func run() error {
	scan := flag.Bool("scan", false, "read-only scan as JSON; never cleans")
	export := flag.Bool("export-history", false, "export history JSON to stdout")
	scheduled := flag.Bool("scheduled-scan", false, "run only a read-only scan and atomically save its snapshot")
	scheduleMode := flag.String("schedule", "", "manage optional read-only scans: daily, weekly, off, status")
	scheduleHour := flag.Int("schedule-hour", 9, "local hour (0-23) for an explicitly installed read-only schedule")
	protect := flag.String("protect-path", "", "add a literal path protection; never authorizes cleanup")
	unprotect := flag.String("unprotect-path", "", "remove a literal path protection; rescan to refresh candidates")
	ver := flag.Bool("version", false, "print version")
	flag.Parse()
	if *ver {
		fmt.Println("devclean", version)
		return nil
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fmt.Errorf("macOS or Linux is required")
	}
	modes := 0
	for _, active := range []bool{*scan, *export, *scheduled, *scheduleMode != "", *protect != "", *unprotect != ""} {
		if active {
			modes++
		}
	}
	if modes > 1 || flag.NArg() > 0 {
		return fmt.Errorf("choose exactly one CLI mode; modes cannot be combined")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir, err := config.Directory()
	if err != nil {
		return err
	}
	h := storage.History{Path: filepath.Join(dir, "history.json")}
	if *export {
		rs, err := h.Load()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(rs)
	}
	s, err := config.Load(filepath.Join(dir, "settings.json"))
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	exec := runner.Exec{}
	settingsPath := filepath.Join(dir, "settings.json")
	if *protect != "" || *unprotect != "" {
		value := *protect
		if value == "" {
			value = *unprotect
		}
		path, e := storage.CanonicalPath(value, home)
		if e != nil {
			return e
		}
		if *protect != "" {
			found := false
			for _, p := range s.ProtectedPaths {
				canonical, e := storage.CanonicalPath(p, home)
				if e != nil {
					return e
				}
				found = found || canonical == path
			}
			if !found {
				s.ProtectedPaths = append(s.ProtectedPaths, path)
			}
		} else {
			var keep []string
			for _, p := range s.ProtectedPaths {
				canonical, e := storage.CanonicalPath(p, home)
				if e != nil {
					return e
				}
				if canonical != path {
					keep = append(keep, p)
				}
			}
			s.ProtectedPaths = keep
		}
		return config.Save(settingsPath, s)
	}
	ps := []storage.Provider{&providers.Docker{Run: exec}}
	if runtime.GOOS == "darwin" {
		ps = append(ps, &providers.Xcode{Home: home, Run: exec, SystemSimulatorRoot: "/Library/Developer/CoreSimulator", Simulators: &providers.Simulators{Home: home, Run: exec}})
	}
	ps = append(ps, &providers.Homebrew{Run: exec}, &providers.Caches{Home: home, Run: exec})
	e := &storage.Engine{Home: home, History: h, Providers: ps, Policy: func() (storage.Policy, error) {
		current, err := config.Load(settingsPath)
		return current.Policy(home), err
	}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *scheduleMode != "" {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		binary, err = filepath.EvalSymlinks(binary)
		if err != nil {
			return err
		}
		manager := schedule.Manager{OS: runtime.GOOS, Home: home, ConfigDir: dir, ConfigHome: filepath.Dir(dir), Executable: binary, Path: os.Getenv("PATH"), UID: os.Getuid(), Run: exec}
		switch *scheduleMode {
		case "status":
			state, err := manager.Status(ctx)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(state)
		case "off":
			return manager.Disable(ctx)
		case "daily", "weekly":
			return manager.Install(ctx, *scheduleMode, *scheduleHour)
		default:
			return fmt.Errorf("schedule must be daily, weekly, off or status")
		}
	}
	if *scan || *scheduled {
		ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
		defer cancel()
		var scanUnlock func()
		if *scheduled {
			scanUnlock, err = storage.FileLock(filepath.Join(dir, "scheduled-scan.lock"))
			if err != nil {
				return err
			}
			defer scanUnlock()
		}
		snap := e.Scan(ctx)
		if *scheduled {
			if err = storage.AtomicJSON(filepath.Join(dir, "scheduled-scan.json"), snap); err != nil {
				return err
			}
			fmt.Println("Read-only scan saved; no cleanup operations were planned or executed.")
			return nil
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snap)
	}
	model := tui.New(e, s, filepath.Join(dir, "settings.json"), ctx)
	_, err = tea.NewProgram(model, tea.WithContext(ctx)).Run()
	stop()
	model.Wait()
	return err
}
