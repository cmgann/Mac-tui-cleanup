package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cmgann/Mac-tui-cleanup/internal/runner"
)

type Engine struct {
	Providers []Provider
	Home      string
	History   History
	Measure   func(string) (Disk, error)
}

func (e *Engine) disk() (Disk, error) {
	if e.Measure != nil {
		return e.Measure(e.Home)
	}
	return DiskUsage(e.Home)
}

func (e *Engine) Scan(ctx context.Context) Snapshot { return e.ScanProgress(ctx, func(string) {}) }
func (e *Engine) ScanProgress(ctx context.Context, progress func(string)) Snapshot {
	s := Snapshot{At: time.Now()}
	var err error
	s.Disk, err = e.disk()
	if err != nil {
		s.Errors = append(s.Errors, err.Error())
	}
	for _, p := range e.Providers {
		progress("Scanning " + p.ID())
		providerCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		cs, err := p.Scan(providerCtx)
		cancel()
		s.Candidates = append(s.Candidates, cs...)
		if err != nil {
			s.Errors = append(s.Errors, p.ID()+": "+err.Error())
		}
		if ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		s.Canceled = true
		s.Errors = append(s.Errors, ctx.Err().Error())
	}
	s.At = time.Now()
	return s
}
func (e *Engine) Plan(ctx context.Context, cs []Candidate, age int) ([]Operation, error) {
	var ops []Operation
	seen := map[string]bool{}
	for _, c := range cs {
		if !c.Selected {
			continue
		}
		if !c.Eligible() {
			return nil, fmt.Errorf("protected candidate %s", c.Name)
		}
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		p := e.provider(c.Provider)
		if p == nil {
			return nil, errors.New("unknown provider")
		}
		op, err := p.Plan(ctx, c, age)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		return nil, errors.New("select eligible candidates first")
	}
	return ops, nil
}
func (e *Engine) provider(id string) Provider {
	for _, p := range e.Providers {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

// Execute is called only after the TUI's review and typed DELETE confirmation.
func (e *Engine) Execute(ctx context.Context, ops []Operation, progress func(string)) []Record {
	return e.ExecuteProgress(ctx, ops, func(event ProgressEvent) {
		if event.Kind == "operation" {
			progress(fmt.Sprintf("%d/%d · %s", event.Index, event.Total, event.Name))
		}
	})
}
func (e *Engine) ExecuteProgress(ctx context.Context, ops []Operation, progress func(ProgressEvent)) []Record {
	var rs []Record
	unlock, lockErr := FileLock(e.History.Path + ".cleanup.lock")
	if lockErr != nil {
		return []Record{{Timestamp: time.Now(), Errors: []string{lockErr.Error()}}}
	}
	defer unlock()
	if _, err := e.History.Load(); err != nil {
		return []Record{{Timestamp: time.Now(), Errors: []string{err.Error()}}}
	}
	completed, failed := 0, 0
	for i, op := range ops {
		if ctx.Err() != nil {
			break
		}
		event := ProgressEvent{Kind: "operation", Provider: op.Candidate.Provider, CandidateID: op.Candidate.ID, Name: op.Candidate.Name, Index: i + 1, Total: len(ops), Completed: completed, Failed: failed, At: time.Now()}
		progress(event)
		var outputMu sync.Mutex
		var commandOutput string
		truncated := false
		operationCtx := runner.WithOutput(ctx, func(out runner.OutputEvent) {
			outputMu.Lock()
			defer outputMu.Unlock()
			text := out.Text
			if out.Kind == "command" {
				text = out.Command + "\n"
			}
			label := out.Stream
			if label == "" {
				label = out.Kind
			}
			commandOutput += "[" + label + "] " + text
			if len(commandOutput) > 65536 {
				commandOutput = commandOutput[len(commandOutput)-65536:]
				truncated = true
			}
			update := event
			update.Kind = out.Kind
			update.Stream = out.Stream
			update.Text = text
			update.At = time.Now()
			progress(update)
		})
		r := Record{ID: fmt.Sprintf("%d-%d", time.Now().UnixNano(), i), Timestamp: time.Now(), Provider: op.Candidate.Provider, Operation: op.Scope, CandidateIDs: []string{op.Candidate.ID}, EstimatedBytes: op.Candidate.EstimatedBytes}
		before, be := e.disk()
		r.AvailableBefore = before.Available
		p := e.provider(r.Provider)
		var result Result
		var err error
		if p == nil {
			err = errors.New("unknown provider")
		} else {
			result, err = p.Clean(operationCtx, op)
		}
		after, ae := e.disk()
		r.AvailableAfter = after.Available
		r.MeasurementValid = be == nil && ae == nil
		if r.MeasurementValid {
			r.DiskDeltaBytes = Delta(before.Available, after.Available)
		}
		outputMu.Lock()
		r.CommandOutput = strings.ToValidUTF8(commandOutput, "�")
		if truncated {
			r.CommandOutput = "[earlier command output omitted; latest 64 KiB follows]\n" + r.CommandOutput
		}
		outputMu.Unlock()
		r.PlanWarning = op.Warning
		r.ProviderUsageBeforeBytes = result.ProviderUsageBeforeBytes
		r.ProviderUsageAfterBytes = result.ProviderUsageAfterBytes
		r.Errors = append(r.Errors, result.MeasurementErrors...)
		r.ProviderReportedBytes = result.ProviderReportedBytes
		r.Output = result.Output
		r.Success = err == nil
		for _, v := range []error{be, err, ae} {
			if v != nil {
				r.Errors = append(r.Errors, v.Error())
			}
		}
		if he := e.History.Append(r); he != nil {
			r.Errors = append(r.Errors, "History could not be saved: "+he.Error())
		}
		completed++
		if !r.Success {
			failed++
		}
		event.Kind = "finished"
		event.Completed = completed
		event.Failed = failed
		event.Status = "success"
		if !r.Success {
			event.Status = "failed"
		}
		if len(r.Errors) > 0 {
			event.Text = strings.Join(r.Errors, "; ")
			if r.Success {
				event.Status = "success (warnings)"
			}
		}
		event.At = time.Now()
		progress(event)
		rs = append(rs, r)
	}
	return rs
}
