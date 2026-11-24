// Package app combines the modules into Portly's use cases.
package app

import (
	"context"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
	"github.com/AnindyaMajumder/Portly/internal/modules/stopper"
)

const (
	scanDeadline       = 3 * time.Second
	listEngineDeadline = 400 * time.Millisecond
	stopEngineDeadline = 3 * time.Second
	// Extra time for answers that were cut off by their own deadline
	engineGrace = 100 * time.Millisecond
)

// Confirm asks the user again before a force kill.
type Confirm = stopper.Confirm

// Scanner sees sockets and processes.
type Scanner interface {
	Scan(ctx context.Context) (domain.Snapshot, error)
	Observe(ctx context.Context, endpoints []domain.Endpoint) (domain.Snapshot, error)
	Reread(holder domain.Holder, id domain.SocketID) (domain.Process, bool, error)
}

// Engines sees containers.
type Engines interface {
	Engines(ctx context.Context) ([]domain.Engine, []string)
}

// Projects finds source directories.
type Projects interface {
	Detect(ctx context.Context, process domain.Process, ancestors []domain.Process) *domain.Project
}

// Stopper decides and acts.
type Stopper interface {
	Decide(snapshot domain.Snapshot, request domain.StopRequest) domain.StopPlan
	Enabled() bool
	Run(ctx context.Context, request domain.StopRequest, target domain.StopTarget, confirm stopper.Confirm) domain.Outcome
}

// App is every use case.
type App struct {
	world    *World
	projects Projects
	stopper  Stopper
}

// New wires the use cases.
func New(world *World, projects Projects, stopper Stopper) *App {
	return &App{world: world, projects: projects, stopper: stopper}
}

// World joins discovery with the container engines.
type World struct {
	scanner Scanner
	engines Engines
}

// NewWorld joins a scanner and the engines.
func NewWorld(scanner Scanner, engines Engines) *World {
	return &World{scanner: scanner, engines: engines}
}

// Observe scans for a stop and asks every engine, in parallel.
func (w *World) Observe(ctx context.Context, endpoints []domain.Endpoint) (domain.Snapshot, error) {
	snapshot, _, err := w.observe(ctx, endpoints)
	return snapshot, err
}

// observe also returns notes for --verbose.
func (w *World) observe(ctx context.Context, endpoints []domain.Endpoint) (domain.Snapshot, []string, error) {
	scan := func(ctx context.Context) (domain.Snapshot, error) { return w.scanner.Observe(ctx, endpoints) }
	return w.join(ctx, scan, stopEngineDeadline)
}

// Look scans without engines.
func (w *World) Look(ctx context.Context) (domain.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, scanDeadline)
	defer cancel()
	return w.scanner.Scan(ctx)
}

// Reread passes straight through, because it runs right before a signal.
func (w *World) Reread(holder domain.Holder, id domain.SocketID) (domain.Process, bool, error) {
	return w.scanner.Reread(holder, id)
}

// list scans for display, with little patience for engines.
func (w *World) list(ctx context.Context) (domain.Snapshot, []string, error) {
	return w.join(ctx, w.scanner.Scan, listEngineDeadline)
}

// join adds engines to a scan. Engines that stay silent are recorded as such.
func (w *World) join(ctx context.Context, scan func(context.Context) (domain.Snapshot, error), patience time.Duration) (domain.Snapshot, []string, error) {
	type answer struct {
		engines []domain.Engine
		notes   []string
	}
	answers := make(chan answer, 1)
	engineCtx, cancelEngines := context.WithTimeout(ctx, patience)
	defer cancelEngines()
	go func() {
		engines, notes := w.engines.Engines(engineCtx)
		answers <- answer{engines, notes}
	}()
	scanCtx, cancelScan := context.WithTimeout(ctx, scanDeadline)
	defer cancelScan()
	snapshot, err := scan(scanCtx)
	if err != nil {
		return domain.Snapshot{}, nil, err
	}
	// Looking for engines may hang on a dead network path
	stuck := time.NewTimer(patience + engineGrace)
	defer stuck.Stop()
	select {
	case found := <-answers:
		snapshot.Engines, snapshot.EnginesSeen = found.engines, domain.Complete
		return snapshot, found.notes, nil
	case <-stuck.C:
		return snapshot, []string{"Looking for container engines took too long"}, nil
	}
}
