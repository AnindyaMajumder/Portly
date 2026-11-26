package stopper

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// fakeWorld is a system the test edits while the stopper runs.
type fakeWorld struct {
	snapshot   domain.Snapshot
	observeErr error
	lookErr    error
	// Runs just before each observation, reread and look
	beforeObserve, beforeReread, beforeLook func(*fakeWorld)
	observed                                int
}

// Observe returns the current snapshot.
func (w *fakeWorld) Observe(context.Context, []domain.Endpoint) (domain.Snapshot, error) {
	w.observed++
	if w.beforeObserve != nil {
		w.beforeObserve(w)
	}
	return w.snapshot, w.observeErr
}

// Look returns the current snapshot without engines.
func (w *fakeWorld) Look(context.Context) (domain.Snapshot, error) {
	if w.beforeLook != nil {
		w.beforeLook(w)
	}
	return w.snapshot, w.lookErr
}

// Reread finds the process and whether it holds the socket now.
func (w *fakeWorld) Reread(holder domain.Holder, id domain.SocketID) (domain.Process, bool, error) {
	if w.beforeReread != nil {
		w.beforeReread(w)
	}
	process, known := w.snapshot.Processes[holder.PID]
	if !known {
		return domain.Process{}, false, errors.New("gone")
	}
	holds := slices.ContainsFunc(w.snapshot.Sockets, func(s domain.Socket) bool { return s.ID.Same(id) && s.HeldBy(holder.PID) })
	return process, holds, nil
}

// exit removes a process and the sockets only it held.
func (w *fakeWorld) exit(pid int) {
	delete(w.snapshot.Processes, pid)
	w.release(pid)
}

// release drops a holder from every socket.
func (w *fakeWorld) release(pid int) {
	var kept []domain.Socket
	for _, socket := range w.snapshot.Sockets {
		socket.Holders = slices.DeleteFunc(slices.Clone(socket.Holders), func(h domain.Holder) bool { return h.PID == pid })
		if len(socket.Holders) > 0 {
			kept = append(kept, socket)
		}
	}
	w.snapshot.Sockets = kept
}

// fakePin records signals instead of sending them.
type fakePin struct {
	lab *lab
	id  domain.ProcessID
}

// Alive is true while the same process exists.
func (p fakePin) Alive() bool {
	process, known := p.lab.world.snapshot.Processes[p.id.PID]
	return !p.lab.deadPins && known && process.Start == p.id.Start
}

// Terminate records a graceful signal.
func (p fakePin) Terminate(approval *domain.Approval) domain.Delivery {
	target, err := approval.Take(domain.Graceful)
	if err != nil || target.Root != p.id {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	p.lab.terminated = append(p.lab.terminated, p.id.PID)
	if p.lab.onTerminate != nil {
		p.lab.onTerminate(p.lab.world)
	}
	return domain.Delivery{Accepted: p.lab.refuseSignal == "", Code: p.lab.refuseSignal, Resumed: target.Suspended}
}

// ForceStop records a kill.
func (p fakePin) ForceStop(approval *domain.Approval) domain.Delivery {
	target, err := approval.Take(domain.Forced)
	if err != nil || !slices.Contains(target.Members, p.id) {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	p.lab.killed = append(p.lab.killed, p.id.PID)
	p.lab.world.exit(p.id.PID)
	return domain.Delivery{Accepted: true, Killed: []int{p.id.PID}}
}

// Close counts released pins.
func (p fakePin) Close() { p.lab.open-- }

// lab is a stopper wired to fakes.
type lab struct {
	world        *fakeWorld
	engine       *fakeEngine
	stopper      *Stopper
	terminated   []int
	killed       []int
	open         int
	asked        int
	answer       bool
	deadPins     bool
	refuseSignal domain.Code
	onTerminate  func(*fakeWorld)
}

// newLab builds a lab around a snapshot.
func newLab(snapshot domain.Snapshot) *lab {
	l := &lab{world: &fakeWorld{snapshot: snapshot}, engine: &fakeEngine{}}
	l.engine.lab = l
	l.onTerminate = func(w *fakeWorld) { w.exit(appPID) }
	pin := func(id domain.ProcessID) (pin, error) {
		if _, known := l.world.snapshot.Processes[id.PID]; !known {
			return nil, domain.NewError(domain.CodeChanged, "gone", "")
		}
		l.open++
		return fakePin{l, id}, nil
	}
	l.stopper = &Stopper{world: l.world, containers: l.engine, pin: pin, gates: openGates}
	return l
}

// stop plans from the current world, then runs the first target.
func (l *lab) stop(t *testing.T, force bool, afterPlan func(*fakeWorld), endpoints ...domain.Endpoint) domain.Outcome {
	t.Helper()
	request := domain.StopRequest{Endpoints: endpoints, Force: force}
	plan := l.stopper.Decide(l.world.snapshot, request)
	if len(plan.Targets) == 0 {
		t.Fatalf("plan refused: %+v", plan.Results)
	}
	if afterPlan != nil {
		afterPlan(l.world)
	}
	var outcome domain.Outcome
	synctest.Test(t, func(t *testing.T) {
		outcome = l.stopper.Run(t.Context(), request, plan.Targets[0], func(context.Context, domain.StopTarget) bool {
			l.asked++
			return l.answer
		})
	})
	if l.open != 0 {
		t.Errorf("%d pins left open", l.open)
	}
	return outcome
}

// edit changes one process of the world.
func edit(pid int, change func(*domain.Process)) func(*fakeWorld) {
	return func(w *fakeWorld) {
		process := w.snapshot.Processes[pid]
		change(&process)
		w.snapshot.Processes[pid] = process
	}
}

// TestRunStops covers the normal path.
func TestRunStops(t *testing.T) {
	l := newLab(world())
	outcome := l.stop(t, false, nil, web)
	want := domain.Outcome{Method: domain.Graceful, ProcessExited: domain.Yes, PortReleased: domain.Yes, Delivered: true}
	if outcome.Code != "" || outcome.Method != want.Method || outcome.ProcessExited != domain.Yes || outcome.PortReleased != domain.Yes || !outcome.Delivered {
		t.Errorf("got %+v", outcome)
	}
	if !slices.Equal(l.terminated, []int{appPID}) || len(l.killed) != 0 || l.asked != 0 {
		t.Errorf("signals %v %v, asked %d", l.terminated, l.killed, l.asked)
	}
}

// raceCases change the world between the prompt and the signal.
var raceCases = []struct {
	name   string
	change func(*fakeWorld)
	want   domain.Code
}{
	{"pid reused", edit(appPID, func(p *domain.Process) { p.Start = "999" }), domain.CodeChanged},
	{"exec", edit(appPID, func(p *domain.Process) { p.Exe = "/bin/other" }), domain.CodeChanged},
	{"socket closed", func(w *fakeWorld) { w.release(appPID) }, domain.CodeChanged},
	{"socket replaced", func(w *fakeWorld) { w.snapshot.Sockets = []domain.Socket{listener(web, "99", appPID)} }, domain.CodeChanged},
	{"socket handed over", func(w *fakeWorld) {
		w.snapshot.Processes[700] = developer(700, shellPID)
		w.snapshot.Sockets = []domain.Socket{listener(web, "77", 700)}
	}, domain.CodeChanged},
	{"endpoint gained", func(w *fakeWorld) { w.snapshot.Sockets = append(w.snapshot.Sockets, listener(api, "99", appPID)) }, domain.CodeChanged},
	{"suspended", edit(appPID, func(p *domain.Process) { p.Suspended = true }), domain.CodeChanged},
	{"debugger attached", edit(appPID, func(p *domain.Process) { p.Tracer = domain.Yes }), domain.CodeDebugged},
	{"second owner", func(w *fakeWorld) {
		w.snapshot.Processes[700] = developer(700, shellPID)
		w.snapshot.Sockets = append(w.snapshot.Sockets, listener(web, "78", 700))
	}, domain.CodeAmbiguous},
	{"hidden owner", func(w *fakeWorld) { w.snapshot.Sockets = append(w.snapshot.Sockets, listener(web, "78")) }, domain.CodeUnresolved},
	{"became supervised", edit(appPID, func(p *domain.Process) { p.Service = domain.Yes }), domain.CodeSupervised},
	{"exited", func(w *fakeWorld) { w.exit(appPID) }, domain.CodeChanged},
	{"observation failed", func(w *fakeWorld) { w.observeErr = errors.New("broken") }, domain.CodeEvidenceUnavailable},
}

// TestChangesAfterThePromptAreRefused covers every change, with and without force.
func TestChangesAfterThePromptAreRefused(t *testing.T) {
	for _, c := range raceCases {
		for _, force := range []bool{false, true} {
			l := newLab(world())
			l.answer = true
			outcome := l.stop(t, force, c.change, web)
			if outcome.Code != c.want || outcome.Delivered || len(l.terminated)+len(l.killed) != 0 || l.asked != 0 {
				t.Errorf("%s force=%v: %+v, signals %v %v", c.name, force, outcome, l.terminated, l.killed)
			}
		}
	}
}

// TestChangesAfterRevalidationAreRefused covers the window before the signal.
func TestChangesAfterRevalidationAreRefused(t *testing.T) {
	for name, change := range map[string]func(*fakeWorld){
		"pid reused":        edit(appPID, func(p *domain.Process) { p.Start = "999" }),
		"socket closed":     func(w *fakeWorld) { w.release(appPID) },
		"exited":            func(w *fakeWorld) { w.exit(appPID) },
		"zombie":            edit(appPID, func(p *domain.Process) { p.Zombie = true }),
		"debugger attached": edit(appPID, func(p *domain.Process) { p.Tracer = domain.Yes }),
		"suspended":         edit(appPID, func(p *domain.Process) { p.Suspended = true }),
	} {
		l := newLab(world())
		l.world.beforeReread = change
		if outcome := l.stop(t, false, nil, web); outcome.Code != domain.CodeChanged || len(l.terminated) != 0 {
			t.Errorf("%s: %+v, signals %v", name, outcome, l.terminated)
		}
	}
}

// TestDeadPinIsNeverSignalled covers a PID reused between pin and reread.
func TestDeadPinIsNeverSignalled(t *testing.T) {
	l := newLab(world())
	l.deadPins = true
	if outcome := l.stop(t, false, nil, web); outcome.Code != domain.CodeChanged || len(l.terminated) != 0 {
		t.Errorf("%+v, signals %v", outcome, l.terminated)
	}
}

// TestEverySocketMustStillBeHeld covers one of two named sockets closing late.
func TestEverySocketMustStillBeHeld(t *testing.T) {
	snapshot := world()
	snapshot.Sockets = append(snapshot.Sockets, listener(api, "78", appPID))
	l := newLab(snapshot)
	l.world.beforeReread = func(w *fakeWorld) { w.snapshot.Sockets = w.snapshot.Sockets[:1] }
	if outcome := l.stop(t, false, nil, web, api); outcome.Code != domain.CodeChanged || len(l.terminated) != 0 {
		t.Errorf("%+v, signals %v", outcome, l.terminated)
	}
}

// TestSignalErrors reports why nothing was delivered.
func TestSignalErrors(t *testing.T) {
	for _, code := range []domain.Code{domain.CodePermissionDenied, domain.CodeChanged} {
		l := newLab(world())
		l.refuseSignal, l.onTerminate, l.answer = code, nil, true
		if outcome := l.stop(t, true, nil, web); outcome.Code != code || outcome.Delivered || len(l.killed) != 0 {
			t.Errorf("%s: %+v, killed %v", code, outcome, l.killed)
		}
	}
}

// TestOutcomes covers every result of verification.
func TestOutcomes(t *testing.T) {
	restart := func(exe string) func(*fakeWorld) {
		return func(w *fakeWorld) {
			w.exit(appPID)
			next := developer(800, 1)
			next.Exe = exe
			w.snapshot.Processes[800] = next
			w.snapshot.Sockets = append(w.snapshot.Sockets, listener(web, "99", 800))
		}
	}
	cases := []struct {
		name   string
		after  func(*fakeWorld)
		want   domain.Code
		target bool
	}{
		{"ignores", func(*fakeWorld) {}, domain.CodeStillListening, true},
		{"rebinds", func(w *fakeWorld) { w.snapshot.Sockets = []domain.Socket{listener(web, "99", appPID)} }, domain.CodeStillListening, true},
		{"inherited by a hidden process", func(w *fakeWorld) {
			w.exit(appPID)
			w.snapshot.Sockets = []domain.Socket{listener(web, "77", 900)}
		}, domain.CodeStillListening, false},
		{"draining", func(w *fakeWorld) { w.release(appPID) }, domain.CodeDraining, false},
		{"restarted", restart("/usr/bin/app"), domain.CodeRestarted, false},
		{"taken over", restart("/usr/bin/other"), domain.CodeTakenOver, false},
		{"unknown holder", func(w *fakeWorld) { w.exit(appPID); w.snapshot.Sockets = []domain.Socket{listener(web, "99")} }, domain.CodeUnknownOutcome, false},
		{"table unreadable", func(w *fakeWorld) { w.snapshot.Ports = nil }, domain.CodeUnknownOutcome, false},
		{"look failed", func(w *fakeWorld) { w.lookErr = errors.New("broken") }, domain.CodeUnknownOutcome, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newLab(world())
			l.onTerminate = c.after
			outcome := l.stop(t, false, nil, web)
			if outcome.Code != c.want || outcome.HeldByTarget != c.target || !outcome.Delivered {
				t.Errorf("got %+v, want %s", outcome, c.want)
			}
		})
	}
}

// TestRestartInsideTheSettleTimeIsNotSuccess covers a slow respawn.
func TestRestartInsideTheSettleTimeIsNotSuccess(t *testing.T) {
	l := newLab(world())
	looks := 0
	l.world.beforeLook = func(w *fakeWorld) {
		if looks++; looks == 2 {
			w.snapshot.Processes[800] = developer(800, 1)
			w.snapshot.Sockets = []domain.Socket{listener(web, "99", 800)}
		}
	}
	if outcome := l.stop(t, false, nil, web); outcome.Code != domain.CodeRestarted {
		t.Errorf("got %+v", outcome)
	}
}

// TestSlowExitStillCounts waits within the deadline.
func TestSlowExitStillCounts(t *testing.T) {
	l := newLab(world())
	l.onTerminate = func(*fakeWorld) {}
	looks := 0
	l.world.beforeLook = func(w *fakeWorld) {
		if looks++; looks == 30 {
			w.exit(appPID)
		}
	}
	if outcome := l.stop(t, false, nil, web); outcome.Code != "" {
		t.Errorf("got %+v", outcome)
	}
}

// TestSuspendedIsResumed discloses the wake-up.
func TestSuspendedIsResumed(t *testing.T) {
	snapshot := world()
	change(&snapshot, appPID, func(p *domain.Process) { p.Suspended = true })
	if outcome := newLab(snapshot).stop(t, false, nil, web); outcome.Code != "" || !outcome.Resumed {
		t.Errorf("got %+v", outcome)
	}
}

// TestForceOnlyAfterFailedGracefulStop covers every condition for a kill.
func TestForceOnlyAfterFailedGracefulStop(t *testing.T) {
	ignore := func(*fakeWorld) {}
	cases := []struct {
		name        string
		force, yes  bool
		after       func(*fakeWorld)
		want        domain.Code
		asked, kill int
	}{
		{"no flag", false, true, ignore, domain.CodeStillListening, 0, 0},
		{"declined", true, false, ignore, domain.CodeStillListening, 1, 0},
		{"confirmed", true, true, ignore, "", 1, 1},
		{"graceful worked", true, true, func(w *fakeWorld) { w.exit(appPID) }, "", 0, 0},
		{"draining", true, true, func(w *fakeWorld) { w.release(appPID) }, domain.CodeDraining, 0, 0},
		{"held by someone hidden", true, true, func(w *fakeWorld) {
			w.exit(appPID)
			w.snapshot.Sockets = []domain.Socket{listener(web, "77", 900)}
		}, domain.CodeStillListening, 0, 0},
		{"taken over", true, true, func(w *fakeWorld) {
			w.exit(appPID)
			other := developer(800, 1)
			other.Exe = "/bin/other"
			w.snapshot.Processes[800] = other
			w.snapshot.Sockets = []domain.Socket{listener(web, "99", 800)}
		}, domain.CodeTakenOver, 0, 0},
		{"unobservable", true, true, func(w *fakeWorld) { w.lookErr = errors.New("broken") }, domain.CodeUnknownOutcome, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newLab(world())
			l.answer, l.onTerminate = c.yes, c.after
			outcome := l.stop(t, c.force, nil, web)
			if outcome.Code != c.want || l.asked != c.asked || len(l.killed) != c.kill {
				t.Errorf("got %+v, asked %d, killed %v", outcome, l.asked, l.killed)
			}
			if c.kill == 1 && (outcome.Method != domain.Forced || !slices.Equal(outcome.Killed, []int{appPID})) {
				t.Errorf("forced outcome %+v", outcome)
			}
		})
	}
}

// TestForceRunsEveryCheckAgain covers changes during the second prompt.
func TestForceRunsEveryCheckAgain(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*fakeWorld)
		want   domain.Code
	}{
		"socket closed":     {func(w *fakeWorld) { w.release(appPID) }, domain.CodeChanged},
		"replaced":          {edit(appPID, func(p *domain.Process) { p.Start = "999" }), domain.CodeChanged},
		"debugger attached": {edit(appPID, func(p *domain.Process) { p.Tracer = domain.Yes }), domain.CodeDebugged},
		"second owner":      {func(w *fakeWorld) { w.snapshot.Sockets = append(w.snapshot.Sockets, listener(web, "78")) }, domain.CodeUnresolved},
	} {
		l := newLab(world())
		l.onTerminate = func(*fakeWorld) {}
		request := domain.StopRequest{Endpoints: []domain.Endpoint{web}, Force: true}
		plan := l.stopper.Decide(l.world.snapshot, request)
		var outcome domain.Outcome
		synctest.Test(t, func(t *testing.T) {
			outcome = l.stopper.Run(t.Context(), request, plan.Targets[0], func(context.Context, domain.StopTarget) bool {
				c.change(l.world)
				return true
			})
		})
		if outcome.Code != c.want || len(l.killed) != 0 {
			t.Errorf("%s: %+v, killed %v", name, outcome, l.killed)
		}
	}
}

// TestForceIgnoresGainedEndpoints compares identity only.
func TestForceIgnoresGainedEndpoints(t *testing.T) {
	l := newLab(world())
	l.answer = true
	l.onTerminate = func(w *fakeWorld) { w.snapshot.Sockets = append(w.snapshot.Sockets, listener(api, "99", appPID)) }
	if outcome := l.stop(t, true, nil, web); outcome.Code != "" || len(l.killed) != 1 {
		t.Errorf("%+v, killed %v", outcome, l.killed)
	}
}

// TestFamilyForceKillsOnlyHolders kills the root first, then remaining holders.
func TestFamilyForceKillsOnlyHolders(t *testing.T) {
	snapshot := family()
	child := developer(650, appPID)
	add(&snapshot, child)
	l := newLab(snapshot)
	l.answer = true
	// Signalled root ignores, and one worker leaves by itself
	l.onTerminate = func(w *fakeWorld) { w.exit(602) }
	outcome := l.stop(t, true, nil, web)
	if outcome.Code != "" || !slices.Equal(l.terminated, []int{appPID}) || !slices.Equal(l.killed, []int{appPID, 601}) {
		t.Errorf("%+v, terminated %v, killed %v", outcome, l.terminated, l.killed)
	}
}

// TestFamilyGracefulSignalsRootOnly never signals a worker.
func TestFamilyGracefulSignalsRootOnly(t *testing.T) {
	l := newLab(family())
	l.onTerminate = func(w *fakeWorld) { w.exit(601); w.exit(602); w.exit(appPID) }
	if outcome := l.stop(t, false, nil, web); outcome.Code != "" || !slices.Equal(l.terminated, []int{appPID}) {
		t.Errorf("%+v, terminated %v", outcome, l.terminated)
	}
}

// TestOrphanedWorkersAreNotKilled covers a root that died and left workers.
func TestOrphanedWorkersAreNotKilled(t *testing.T) {
	l := newLab(family())
	l.answer = true
	l.onTerminate = func(w *fakeWorld) { w.exit(appPID) }
	outcome := l.stop(t, true, nil, web)
	if outcome.Code != domain.CodeChanged || len(l.killed) != 0 {
		t.Errorf("%+v, killed %v", outcome, l.killed)
	}
	if !outcome.Delivered || outcome.Method != domain.Graceful {
		t.Errorf("the delivered SIGTERM was lost: %+v", outcome)
	}
}

// TestFamilyWithLivingWorkersIsDraining needs every member gone.
func TestFamilyWithLivingWorkersIsDraining(t *testing.T) {
	l := newLab(family())
	l.onTerminate = func(w *fakeWorld) {
		w.exit(appPID)
		w.release(601)
		w.release(602)
		// Holders that closed their socket lose their details
		w.snapshot.Processes[601] = domain.Process{ProcessID: domain.ProcessID{PID: 601, Start: "601"}}
	}
	outcome := l.stop(t, false, nil, web)
	if outcome.Code != domain.CodeDraining || outcome.ProcessExited != domain.No || outcome.PortReleased != domain.Yes {
		t.Errorf("got %+v", outcome)
	}
}

// TestNewWorkerMustBeGoneToo covers a worker replaced while the prompt was open.
func TestNewWorkerMustBeGoneToo(t *testing.T) {
	l := newLab(family())
	replaceWorker := func(w *fakeWorld) {
		w.exit(602)
		w.snapshot.Processes[603] = developer(603, appPID)
		w.snapshot.Sockets = []domain.Socket{listener(web, "77", appPID, 601, 603)}
	}
	l.onTerminate = func(w *fakeWorld) {
		w.exit(appPID)
		w.exit(601)
		w.release(603)
	}
	if outcome := l.stop(t, false, replaceWorker, web); outcome.Code != domain.CodeDraining || outcome.ProcessExited != domain.No {
		t.Errorf("got %+v", outcome)
	}
}

// TestPartialVerificationIsUnknown never reads a missing process as exited.
func TestPartialVerificationIsUnknown(t *testing.T) {
	l := newLab(world())
	l.onTerminate = func(w *fakeWorld) {
		w.exit(appPID)
		w.snapshot.Host.ProcessTable = domain.Partial
	}
	if outcome := l.stop(t, false, nil, web); outcome.Code != domain.CodeUnknownOutcome {
		t.Errorf("got %+v", outcome)
	}
}

// TestEnabled follows the platform gate.
func TestEnabled(t *testing.T) {
	if !(&Stopper{gates: openGates}).Enabled() || (&Stopper{}).Enabled() {
		t.Error("enabled must follow the gate")
	}
}

// TestDescriptorIsPassedToReread keeps the last read short.
func TestDescriptorIsPassedToReread(t *testing.T) {
	snapshot := world()
	if got := descriptorOf(snapshot, appPID, snapshot.Sockets[0].ID); got != 3 {
		t.Errorf("got %d", got)
	}
	if got := descriptorOf(snapshot, 700, snapshot.Sockets[0].ID); got != -1 {
		t.Errorf("unknown holder: %d", got)
	}
}

// TestMissingConfirmationNeverForces covers a caller without a prompt.
func TestMissingConfirmationNeverForces(t *testing.T) {
	l := newLab(world())
	l.onTerminate = func(*fakeWorld) {}
	request := domain.StopRequest{Endpoints: []domain.Endpoint{web}, Force: true}
	plan := l.stopper.Decide(l.world.snapshot, request)
	var outcome domain.Outcome
	synctest.Test(t, func(t *testing.T) { outcome = l.stopper.Run(t.Context(), request, plan.Targets[0], nil) })
	if outcome.Code != domain.CodeStillListening || len(l.killed) != 0 {
		t.Errorf("%+v, killed %v", outcome, l.killed)
	}
}

// TestForcedKillNeedsAPlannedSocket refuses a root that only holds new ones.
func TestForcedKillNeedsAPlannedSocket(t *testing.T) {
	snapshot := world()
	snapshot.Sockets = append(snapshot.Sockets, listener(web, "78", appPID))
	l := newLab(snapshot)
	l.answer = true
	l.onTerminate = func(*fakeWorld) {}
	reads := 0
	l.world.beforeReread = func(w *fakeWorld) {
		// The graceful stop reread both sockets, the kill comes next
		if reads++; reads == 3 {
			w.snapshot.Sockets = []domain.Socket{listener(web, "99", appPID)}
		}
	}
	if outcome := l.stop(t, true, nil, web); outcome.Code != domain.CodeChanged || len(l.killed) != 0 {
		t.Errorf("%+v, killed %v", outcome, l.killed)
	}
}

// TestNoGracefulMethodNeedsForce kills only with --force and a second confirmation.
func TestNoGracefulMethodNeedsForce(t *testing.T) {
	for _, answer := range []bool{true, false} {
		l := newLab(world())
		l.stopper.gates = gates{native: true}
		l.answer = answer
		outcome := l.stop(t, true, nil, web)
		wantKilled, wantCode := 1, domain.Code("")
		if !answer {
			wantKilled, wantCode = 0, domain.CodeCancelled
		}
		if outcome.Code != wantCode || l.asked != 1 || len(l.terminated) != 0 || len(l.killed) != wantKilled {
			t.Errorf("answer %v: %+v, asked %d, %v %v", answer, outcome, l.asked, l.terminated, l.killed)
		}
	}
	l := newLab(world())
	l.stopper.gates = gates{native: true}
	target := domain.StopTarget{Kind: domain.KindProcess}
	if got := l.stopper.Run(t.Context(), domain.StopRequest{}, target, nil); got.Code != domain.CodeNoGraceful {
		t.Errorf("without force: %+v", got)
	}
}

// TestCancelBeforeSignalSendsNothing covers Ctrl-C during revalidation.
func TestCancelBeforeSignalSendsNothing(t *testing.T) {
	for _, hook := range []string{"observe", "pre-signal"} {
		l := newLab(world())
		request := domain.StopRequest{Endpoints: []domain.Endpoint{web}}
		plan := l.stopper.Decide(l.world.snapshot, request)
		ctx, cancel := context.WithCancel(t.Context())
		if hook == "observe" {
			l.world.beforeObserve = func(*fakeWorld) { cancel() }
		} else {
			cancel()
		}
		outcome := l.stopper.Run(ctx, request, plan.Targets[0], nil)
		cancel()
		if outcome.Code != domain.CodeCancelled || len(l.terminated) != 0 {
			t.Errorf("%s: %+v", hook, outcome)
		}
	}
}

// TestCancelAfterSignalIsUnknown covers Ctrl-C during verification.
func TestCancelAfterSignalIsUnknown(t *testing.T) {
	for _, exits := range []bool{true, false} {
		l := newLab(world())
		l.answer = true
		request := domain.StopRequest{Endpoints: []domain.Endpoint{web}, Force: true}
		plan := l.stopper.Decide(l.world.snapshot, request)
		ctx, cancel := context.WithCancel(t.Context())
		l.onTerminate = func(w *fakeWorld) {
			if exits {
				w.exit(appPID)
			}
			cancel()
		}
		outcome := l.stopper.Run(ctx, request, plan.Targets[0], func(context.Context, domain.StopTarget) bool { return true })
		if outcome.Code != domain.CodeUnknownOutcome || !outcome.Delivered || len(l.killed) != 0 {
			t.Errorf("%+v, killed %v", outcome, l.killed)
		}
	}
}

// TestClosedGateBlocksRun proves a plan made elsewhere can't pass a closed gate.
func TestClosedGateBlocksRun(t *testing.T) {
	l := newLab(world())
	request := domain.StopRequest{Endpoints: []domain.Endpoint{web}}
	plan := l.stopper.Decide(l.world.snapshot, request)
	l.stopper.gates.native = false
	if outcome := l.stopper.Run(t.Context(), request, plan.Targets[0], nil); outcome.Code != domain.CodePlatform || len(l.terminated) != 0 {
		t.Errorf("%+v", outcome)
	}
}

// TestVerificationTakesAtLeastTheSettleTime proves success needs two looks.
func TestVerificationTakesAtLeastTheSettleTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, checks := time.Now(), 0
		waitFor(t.Context(), processDeadline, func() (domain.Outcome, bool) { checks++; return domain.Outcome{}, true })
		if time.Since(started) != settleTime || checks != 2 {
			t.Errorf("%v, %d checks", time.Since(started), checks)
		}
	})
}
