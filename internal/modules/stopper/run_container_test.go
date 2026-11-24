package stopper

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// fakeEngine records container calls instead of making them.
type fakeEngine struct {
	lab            *lab
	stops, kills   int
	delivery       domain.Delivery
	state          domain.ContainerState
	onStop, onKill func(*fakeEngine)
}

// Stop records a graceful stop.
func (e *fakeEngine) Stop(_ context.Context, approval *domain.Approval) domain.Delivery {
	if _, err := approval.Take(domain.Graceful); err != nil {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	e.stops++
	if e.onStop != nil {
		e.onStop(e)
	}
	return e.delivery
}

// ForceStop records a kill.
func (e *fakeEngine) ForceStop(_ context.Context, approval *domain.Approval) domain.Delivery {
	if _, err := approval.Take(domain.Forced); err != nil {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	e.kills++
	if e.onKill != nil {
		e.onKill(e)
	}
	return domain.Delivery{Accepted: true}
}

// State returns the scripted state.
func (e *fakeEngine) State(context.Context, domain.ContainerRef) domain.ContainerState {
	return e.state
}

// gone ends the container and its host socket.
func gone(e *fakeEngine) {
	e.state = domain.ContainerState{Known: true}
	e.lab.world.snapshot.Sockets = nil
	e.lab.world.snapshot.Engines = []domain.Engine{{ID: "engine-1", Kind: "docker", OwnerUID: "0", Answer: domain.AnswerOK}}
}

// containerLab runs one published container that stops when asked.
func containerLab() *lab {
	snapshot := world()
	published(&snapshot)
	l := newLab(snapshot)
	l.engine.delivery = domain.Delivery{Accepted: true}
	l.engine.state = domain.ContainerState{Known: true, Running: true, StartedAt: "t1"}
	l.engine.onStop, l.engine.onKill = gone, gone
	return l
}

// TestContainerStop asks the engine and never signals a process.
func TestContainerStop(t *testing.T) {
	l := containerLab()
	outcome := l.stop(t, false, nil, web)
	if outcome.Code != "" || outcome.Method != domain.Graceful || outcome.ProcessExited != domain.Yes || l.engine.stops != 1 || l.engine.kills != 0 {
		t.Errorf("%+v, stops %d, kills %d", outcome, l.engine.stops, l.engine.kills)
	}
	if len(l.terminated)+len(l.killed) != 0 {
		t.Errorf("a process was signalled: %v %v", l.terminated, l.killed)
	}
}

// TestContainerOutcomes covers every result.
func TestContainerOutcomes(t *testing.T) {
	cases := []struct {
		name string
		stop func(*fakeEngine)
		want domain.Code
	}{
		{"ignores the signal", func(*fakeEngine) {}, domain.CodePending},
		{"restarted", func(e *fakeEngine) { e.state.StartedAt = "t2" }, domain.CodeRestarted},
		{"engine silent", func(e *fakeEngine) { e.state.Known = false }, domain.CodeUnknownOutcome},
		{"socket without holder stays", func(e *fakeEngine) { e.state.Running = false }, domain.CodeUnknownOutcome},
		{"taken over", func(e *fakeEngine) {
			gone(e)
			e.lab.world.snapshot.Sockets = []domain.Socket{listener(web, "99", appPID)}
		}, domain.CodeTakenOver},
		{"table unreadable", func(e *fakeEngine) { gone(e); e.lab.world.snapshot.Ports = nil }, domain.CodeUnknownOutcome},
	}
	for _, c := range cases {
		l := containerLab()
		l.engine.onStop = c.stop
		if outcome := l.stop(t, false, nil, web); outcome.Code != c.want || !outcome.Delivered {
			t.Errorf("%s: %+v", c.name, outcome)
		}
	}
}

// TestContainerChangesAreRefused covers changes after the prompt.
func TestContainerChangesAreRefused(t *testing.T) {
	cases := map[string]struct {
		change func(*fakeWorld)
		want   domain.Code
	}{
		"restarted":       {func(w *fakeWorld) { editContainer(&w.snapshot, func(c *domain.Container) { c.StartedAt = "t2" }) }, domain.CodeChanged},
		"paused":          {func(w *fakeWorld) { editContainer(&w.snapshot, func(c *domain.Container) { c.Paused = true }) }, domain.CodeChanged},
		"engine moved":    {func(w *fakeWorld) { delete(w.snapshot.Processes, initPID) }, domain.CodeAmbiguous},
		"native intruder": {func(w *fakeWorld) { w.snapshot.Sockets = append(w.snapshot.Sockets, listener(web, "90", appPID)) }, domain.CodeAmbiguous},
		"swarm":           {func(w *fakeWorld) { editContainer(&w.snapshot, func(c *domain.Container) { c.Orchestrator = "swarm" }) }, domain.CodeContainerManaged},
		"binding gained": {func(w *fakeWorld) {
			editContainer(&w.snapshot, func(c *domain.Container) {
				c.Ports = append(c.Ports, domain.Binding{HostAddr: anyAddress, HostPort: 9000, Protocol: domain.TCP})
			})
		}, domain.CodeChanged},
	}
	for name, c := range cases {
		l := containerLab()
		if outcome := l.stop(t, false, c.change, web); outcome.Code != c.want || l.engine.stops != 0 {
			t.Errorf("%s: %+v, stops %d", name, outcome, l.engine.stops)
		}
	}
}

// TestReplacedContainerIsNotSuccess covers a new container without a host socket.
func TestReplacedContainerIsNotSuccess(t *testing.T) {
	cases := map[string]struct {
		edit func(*fakeEngine, domain.Engine)
		want domain.Code
	}{
		"recreated": {func(e *fakeEngine, old domain.Engine) {
			old.Containers[0].ID, old.Containers[0].StartedAt = "new", "t9"
			e.lab.world.snapshot.Engines = []domain.Engine{old}
		}, domain.CodeRestarted},
		"another one": {func(e *fakeEngine, old domain.Engine) {
			old.Containers[0].ID, old.Containers[0].Name = "new", "other"
			e.lab.world.snapshot.Engines = []domain.Engine{old}
		}, domain.CodeTakenOver},
		"engine went silent": {func(e *fakeEngine, old domain.Engine) {
			e.lab.world.snapshot.Engines[0].Answer = domain.AnswerTimeout
		}, domain.CodeUnknownOutcome},
		"engines not checked": {func(e *fakeEngine, old domain.Engine) { e.lab.world.snapshot.EnginesSeen = domain.Partial }, domain.CodeUnknownOutcome},
		"cannot observe":      {func(e *fakeEngine, old domain.Engine) { e.lab.world.observeErr = errors.New("broken") }, domain.CodeUnknownOutcome},
	}
	for name, c := range cases {
		l := containerLab()
		l.engine.onStop = func(e *fakeEngine) {
			old := e.lab.world.snapshot.Engines[0]
			old.Containers = slices.Clone(old.Containers)
			gone(e)
			c.edit(e, old)
		}
		if outcome := l.stop(t, false, nil, web); outcome.Code != c.want {
			t.Errorf("%s: %+v", name, outcome)
		}
	}
}

// TestClosedEngineDoesNotHideSuccess covers Docker next to root's Podman.
func TestClosedEngineDoesNotHideSuccess(t *testing.T) {
	l := containerLab()
	closed := domain.Engine{ID: "/run/podman/podman.sock", OwnerUID: "0", Answer: domain.AnswerDenied}
	l.world.snapshot.Engines = append(l.world.snapshot.Engines, closed)
	l.engine.onStop = func(e *fakeEngine) {
		gone(e)
		e.lab.world.snapshot.Engines = append(e.lab.world.snapshot.Engines, closed)
	}
	if outcome := l.stop(t, false, nil, web); outcome.Code != "" {
		t.Errorf("got %+v", outcome)
	}
}

// TestFirstForceStopChecksReach covers a kill with no graceful stop before it.
func TestFirstForceStopChecksReach(t *testing.T) {
	for name, edit := range map[string]func(*domain.Container){
		"paused": func(c *domain.Container) { c.Paused = true },
		"gained a port": func(c *domain.Container) {
			c.Ports = append(c.Ports, domain.Binding{HostAddr: anyAddress, HostPort: 9000, Protocol: domain.TCP})
		},
	} {
		l := containerLab()
		editContainer(&l.world.snapshot, func(c *domain.Container) { c.StopSignal = "SIGKILL" })
		outcome := l.stop(t, true, func(w *fakeWorld) { editContainer(&w.snapshot, edit) }, web)
		if outcome.Code != domain.CodeChanged || l.engine.kills != 0 {
			t.Errorf("%s: %+v, kills %d", name, outcome, l.engine.kills)
		}
	}
}

// TestRejectedStopNeverBecomesAKill covers t=-1 rejection and engine errors.
func TestRejectedStopNeverBecomesAKill(t *testing.T) {
	for _, code := range []domain.Code{domain.CodeEngineRejected, domain.CodeEngineUnresponsive, domain.CodeUnknownOutcome, domain.CodeChanged} {
		l := containerLab()
		l.answer, l.engine.onStop = true, nil
		l.engine.delivery = domain.Delivery{Code: code}
		outcome := l.stop(t, true, nil, web)
		if outcome.Code != code || l.engine.stops != 1 || l.engine.kills != 0 || l.asked != 0 {
			t.Errorf("%s: %+v, stops %d, kills %d, asked %d", code, outcome, l.engine.stops, l.engine.kills, l.asked)
		}
	}
}

// TestPendingContainerCanBeForced needs the flag and a confirmation.
func TestPendingContainerCanBeForced(t *testing.T) {
	for _, c := range []struct {
		force, yes bool
		want       domain.Code
		kills      int
	}{{false, true, domain.CodePending, 0}, {true, false, domain.CodePending, 0}, {true, true, "", 1}} {
		l := containerLab()
		l.answer, l.engine.onStop = c.yes, nil
		outcome := l.stop(t, c.force, nil, web)
		if outcome.Code != c.want || l.engine.kills != c.kills {
			t.Errorf("%+v: %+v, kills %d", c, outcome, l.engine.kills)
		}
	}
}

// TestCancelledContainerStopSendsNothing covers Ctrl-C before the call.
func TestCancelledContainerStopSendsNothing(t *testing.T) {
	l := containerLab()
	request := domain.StopRequest{Endpoints: []domain.Endpoint{web}}
	plan := l.stopper.Decide(l.world.snapshot, request)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if outcome := l.stopper.Run(ctx, request, plan.Targets[0], nil); outcome.Code != domain.CodeCancelled || l.engine.stops != 0 {
		t.Errorf("%+v", outcome)
	}
}
