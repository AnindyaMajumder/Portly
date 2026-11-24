package app

import (
	"context"

	"github.com/AnindyaMajumder/Portly/internal/domain"
	"github.com/AnindyaMajumder/Portly/internal/modules/stopper"
)

// Target is a stop target with what people recognize it by.
type Target struct {
	Stop      domain.StopTarget
	Process   *domain.Process
	Container *domain.Container
	Project   *domain.Project
	Children  []domain.Process
	Proven    bool
}

// Plan is one stop command, decided but not executed.
type Plan struct {
	Request  domain.StopRequest
	Stop     domain.StopPlan
	Targets  []Target
	Warnings []string
	Notes    []string
}

// Plan decides what a stop would do. It changes nothing.
func (a *App) Plan(ctx context.Context, request domain.StopRequest, expect string) (Plan, error) {
	snapshot, notes, err := a.world.observe(ctx, request.Endpoints)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Request: request, Stop: a.stopper.Decide(snapshot, request), Warnings: snapshot.Warnings, Notes: notes}
	for _, target := range plan.Stop.Targets {
		plan.Targets = append(plan.Targets, a.describeTarget(ctx, snapshot, target))
	}
	// With nothing to stop, the refusals themselves say why
	if expect != "" && expect != plan.Stop.ID && len(plan.Targets) > 0 {
		return plan, domain.NewError(domain.CodeChanged, "The targets are not the ones you expected, nothing was stopped",
			"Check the targets again before stopping them")
	}
	return plan, nil
}

// describeTarget adds names and the project, for display only.
func (a *App) describeTarget(ctx context.Context, snapshot domain.Snapshot, target domain.StopTarget) Target {
	described := Target{Stop: target}
	for _, child := range target.Children {
		described.Children = append(described.Children, snapshot.Processes[child.PID])
	}
	if target.Kind != domain.KindContainer {
		process := snapshot.Processes[target.Root.PID]
		detectCtx, cancel := context.WithTimeout(ctx, scanDeadline)
		defer cancel()
		described.Process, described.Project = &process, a.detect(detectCtx, snapshot, process)
		return described
	}
	for _, engine := range snapshot.Engines {
		for _, container := range engine.Containers {
			if container.ID == target.Container.ID && engine.ID == target.Container.Engine {
				owner := containerOwner(snapshot, engine, container, nil)
				described.Container, described.Project, described.Proven = owner.Container, owner.Project, owner.Proven
			}
		}
	}
	return described
}

// CanStop reports whether this build may stop anything on this platform.
func (a *App) CanStop() bool { return a.stopper.Enabled() }

// Execute stops every target in order. A cancelled run sends nothing more.
func (a *App) Execute(ctx context.Context, plan Plan, confirm stopper.Confirm) []domain.Outcome {
	outcomes := make([]domain.Outcome, len(plan.Stop.Targets))
	for i, target := range plan.Stop.Targets {
		if ctx.Err() != nil {
			outcomes[i] = domain.Outcome{Code: domain.CodeCancelled}
			continue
		}
		outcomes[i] = a.stopper.Run(ctx, plan.Request, target, confirm)
	}
	return outcomes
}
