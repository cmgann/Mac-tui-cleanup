package main

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/cmgann/Mac-tui-cleanup/internal/config"
	"github.com/cmgann/Mac-tui-cleanup/internal/providers"
	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
	"github.com/cmgann/Mac-tui-cleanup/internal/storage"
	"github.com/cmgann/Mac-tui-cleanup/internal/tui"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
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
	ver := flag.Bool("version", false, "print version")
	flag.Parse()
	if *ver {
		fmt.Println("devclean", version)
		return nil
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("macOS is required")
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
	e := &storage.Engine{Home: home, History: h, Providers: []storage.Provider{&providers.Docker{Run: exec}, &providers.Xcode{Home: home, Run: exec, SystemSimulatorRoot: "/Library/Developer/CoreSimulator"}, &providers.Homebrew{Run: exec}, &providers.Caches{Home: home, Run: exec}}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *scan {
		ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
		defer cancel()
		snap := e.Scan(ctx)
		for i := range snap.Candidates {
			for _, id := range s.Pinned {
				if snap.Candidates[i].ID == id {
					snap.Candidates[i].Protected = true
					snap.Candidates[i].ProtectionReason = "Pinned in settings"
				}
			}
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
