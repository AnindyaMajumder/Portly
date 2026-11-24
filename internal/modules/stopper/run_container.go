package stopper

import (
	"context"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// attemptContainer revalidates, then asks the engine. It never signals a process.
func (s *Stopper) attemptContainer(ctx context.Context, request domain.StopRequest, planned domain.StopTarget, method domain.Method) domain.Outcome {
	current, snapshot, outcome := s.revalidate(ctx, request, planned)
	if outcome.Code != "" {
		return outcome
	}
	approval, err := domain.Approve(planned, current, method)
	if err != nil {
		return refusedFor(err)
	}
	if ctx.Err() != nil {
		return refused(domain.Refusal{Code: domain.CodeCancelled})
	}
	var delivery domain.Delivery
	if method == domain.Forced {
		delivery = s.containers.ForceStop(ctx, approval)
	} else {
		delivery = s.containers.Stop(ctx, approval)
	}
	if !delivery.Accepted {
		return domain.Outcome{Code: delivery.Code, Method: method, Delivered: delivery.Code == domain.CodeUnknownOutcome}
	}
	verified := s.verifyContainer(ctx, planned, snapshot)
	verified.Method, verified.Delivered, verified.Resumed = method, true, planned.Paused
	return verified
}

// verifyContainer polls the engine and the port until both are done.
func (s *Stopper) verifyContainer(ctx context.Context, planned domain.StopTarget, before domain.Snapshot) domain.Outcome {
	return waitFor(ctx, containerDeadline, func() (domain.Outcome, bool) {
		state := s.containers.State(ctx, planned.Container)
		snapshot, err := s.world.Look(ctx)
		sockets, isReadable := socketsOn(snapshot, endpointsOf(planned))
		if !state.Known || err != nil || !isReadable {
			return domain.Outcome{Code: domain.CodeUnknownOutcome}, false
		}
		outcome := domain.Outcome{ProcessExited: domain.TriOf(!state.Running), PortReleased: domain.TriOf(len(sockets) == 0)}
		switch {
		case state.Running && state.StartedAt == planned.Container.StartedAt:
			outcome.Code, outcome.HeldByTarget = domain.CodePending, true
		case state.Running:
			outcome.Code = domain.CodeRestarted
		case len(sockets) > 0 && len(domain.HolderPIDs(sockets)) == 0:
			outcome.Code = domain.CodeUnknownOutcome
		case len(sockets) > 0:
			outcome.Code = domain.CodeTakenOver
		default:
			outcome.Code = s.replacement(ctx, planned, before)
		}
		return outcome, outcome.Code == ""
	})
}

// replacement finds a new container on the same port, which may have no host socket.
func (s *Stopper) replacement(ctx context.Context, planned domain.StopTarget, before domain.Snapshot) domain.Code {
	now, err := s.world.Observe(ctx, endpointsOf(planned))
	if err != nil || now.EnginesSeen != domain.Complete || hasUnansweredEngine(now, ownerOfEngine(before, planned.Container.Engine)) {
		return domain.CodeUnknownOutcome
	}
	for _, endpoint := range endpointsOf(planned) {
		next := now.Publishers(endpoint)
		if len(next) == 0 {
			continue
		}
		if next[0].Container.Name == nameOf(before, planned.Container) {
			return domain.CodeRestarted
		}
		return domain.CodeTakenOver
	}
	return ""
}

// ownerOfEngine returns the user an engine runs as.
func ownerOfEngine(snapshot domain.Snapshot, id string) string {
	for _, engine := range snapshot.Engines {
		if engine.ID == id {
			return engine.OwnerUID
		}
	}
	return ""
}

// nameOf finds a container's name, which survives being recreated.
func nameOf(snapshot domain.Snapshot, wanted domain.ContainerRef) string {
	for _, engine := range snapshot.Engines {
		for _, container := range engine.Containers {
			if container.ID == wanted.ID {
				return container.Name
			}
		}
	}
	return ""
}

const (
	pollInterval    = 100 * time.Millisecond
	settleTime      = 1 * time.Second
	processDeadline = 5 * time.Second
	// The engine call itself already waited for the exit
	containerDeadline = 3 * time.Second
)

// waitFor polls until the check passes twice, settleTime apart, or time is up.
func waitFor(ctx context.Context, limit time.Duration, check func() (domain.Outcome, bool)) domain.Outcome {
	deadline := time.Now().Add(limit)
	for {
		outcome, isDone := check()
		if isDone {
			if !sleep(ctx, settleTime) {
				return domain.Outcome{Code: domain.CodeUnknownOutcome}
			}
			if outcome, isDone = check(); isDone {
				return outcome
			}
		}
		if ctx.Err() != nil {
			return domain.Outcome{Code: domain.CodeUnknownOutcome}
		}
		if !time.Now().Before(deadline) {
			return outcome
		}
		if !sleep(ctx, pollInterval) {
			return domain.Outcome{Code: domain.CodeUnknownOutcome}
		}
	}
}

// sleep returns false when the context ends first.
func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
