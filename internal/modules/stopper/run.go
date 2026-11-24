package stopper

import (
	"context"
	"slices"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// World is the only way the stopper sees the system.
type World interface {
	// Observe is slow: two scans and the container engines
	Observe(ctx context.Context, endpoints []domain.Endpoint) (domain.Snapshot, error)
	// Look is one scan without engines, for verification
	Look(ctx context.Context) (domain.Snapshot, error)
	// Reread is fast: one process and one descriptor
	Reread(holder domain.Holder, id domain.SocketID) (domain.Process, bool, error)
}

// ContainerStopper acts on containers through their engine.
type ContainerStopper interface {
	Stop(ctx context.Context, approval *domain.Approval) domain.Delivery
	ForceStop(ctx context.Context, approval *domain.Approval) domain.Delivery
	State(ctx context.Context, container domain.ContainerRef) domain.ContainerState
}

// pin keeps one process identifiable until it is closed.
type pin interface {
	Alive() bool
	Terminate(approval *domain.Approval) domain.Delivery
	ForceStop(approval *domain.Approval) domain.Delivery
	Close()
}

type pinFunc func(domain.ProcessID) (pin, error)

// Confirm asks the user again before a force kill.
type Confirm func(ctx context.Context, target domain.StopTarget) bool

// Stopper decides, revalidates, signals and verifies.
type Stopper struct {
	world      World
	containers ContainerStopper
	pin        pinFunc
	gates      gates
}

// New returns a stopper with this platform's gates.
func New(world World, containers ContainerStopper) *Stopper {
	return &Stopper{world: world, containers: containers, pin: pinProcess, gates: withEvidence(platformGates())}
}

// Enabled reports whether this platform's gate is open.
func (s *Stopper) Enabled() bool { return s.gates.native }

// Decide plans one stop command. It reads nothing but the snapshot.
func (s *Stopper) Decide(snapshot domain.Snapshot, request domain.StopRequest) domain.StopPlan {
	return decide(snapshot, request, s.gates)
}

// Run stops one confirmed target. Force is tried only after a delivered, failed stop.
func (s *Stopper) Run(ctx context.Context, request domain.StopRequest, planned domain.StopTarget, confirm Confirm) domain.Outcome {
	if !planned.GracefulAvailable {
		if !request.Force {
			return refused(domain.Refusal{Code: domain.CodeNoGraceful})
		}
		return s.try(ctx, request, planned, domain.Forced)
	}
	outcome := s.try(ctx, request, planned, domain.Graceful)
	isHeld := outcome.Code == domain.CodeStillListening && outcome.HeldByTarget
	canForce := outcome.Delivered && (isHeld || outcome.Code == domain.CodePending)
	if !request.Force || !canForce || ctx.Err() != nil || confirm == nil || !confirm(ctx, planned) {
		return outcome
	}
	forced := s.try(ctx, request, planned, domain.Forced)
	forced.Resumed = forced.Resumed || outcome.Resumed
	if !forced.Delivered {
		// The graceful stop did happen, and is still reported
		forced.Delivered, forced.Method = true, outcome.Method
		forced.ProcessExited, forced.PortReleased = outcome.ProcessExited, outcome.PortReleased
	}
	return forced
}

// try runs one method against a container or a process.
func (s *Stopper) try(ctx context.Context, request domain.StopRequest, planned domain.StopTarget, method domain.Method) domain.Outcome {
	if planned.Kind == domain.KindContainer {
		return s.attemptContainer(ctx, request, planned, method)
	}
	return s.attemptProcess(ctx, request, planned, method)
}

// attemptProcess pins first, so nothing read later can be another process.
func (s *Stopper) attemptProcess(ctx context.Context, request domain.StopRequest, planned domain.StopTarget, method domain.Method) domain.Outcome {
	pins, err := s.pinTargets(planned, method)
	defer closeAll(pins)
	if err != nil {
		return refusedFor(err)
	}
	current, snapshot, outcome := s.revalidate(ctx, request, planned)
	if outcome.Code != "" {
		return outcome
	}
	program := programOf(snapshot.Processes[current.Root.PID])
	delivery := s.deliver(ctx, attempt{planned, current, snapshot, method}, pins)
	if !delivery.Accepted {
		return refused(domain.Refusal{Code: delivery.Code})
	}
	// A worker that appeared since the prompt must be gone too
	watched := planned
	watched.Members = union(planned.Members, current.Members)
	verified := s.verifyProcess(ctx, watched, program, pins[planned.Root].Alive)
	verified.Method, verified.Delivered = method, true
	verified.Resumed, verified.Killed = delivery.Resumed, delivery.Killed
	return verified
}

// union joins two member lists without repeats.
func union(planned, current []domain.ProcessID) []domain.ProcessID {
	joined := slices.Clone(planned)
	for _, member := range current {
		if !slices.Contains(joined, member) {
			joined = append(joined, member)
		}
	}
	return joined
}

// pinTargets pins the root, and every member before a force kill.
func (s *Stopper) pinTargets(planned domain.StopTarget, method domain.Method) (map[domain.ProcessID]pin, error) {
	wanted := []domain.ProcessID{planned.Root}
	if method == domain.Forced {
		wanted = append(wanted, planned.Members...)
	}
	pins := map[domain.ProcessID]pin{}
	for _, id := range wanted {
		if pins[id] != nil {
			continue
		}
		pinned, err := s.pin(id)
		if err == nil {
			pins[id] = pinned
		}
		// A worker that already exited needs no pin
		if err != nil && id == planned.Root {
			return pins, err
		}
	}
	return pins, nil
}

// closeAll releases every pin.
func closeAll(pins map[domain.ProcessID]pin) {
	for _, pinned := range pins {
		pinned.Close()
	}
}

// revalidate runs the policy on a new snapshot and finds the same target.
func (s *Stopper) revalidate(ctx context.Context, request domain.StopRequest, planned domain.StopTarget) (domain.StopTarget, domain.Snapshot, domain.Outcome) {
	snapshot, err := s.world.Observe(ctx, request.Endpoints)
	if ctx.Err() != nil {
		return domain.StopTarget{}, snapshot, refused(domain.Refusal{Code: domain.CodeCancelled})
	}
	if err != nil {
		return domain.StopTarget{}, snapshot, refused(domain.Refusal{Code: domain.CodeEvidenceUnavailable})
	}
	plan := s.Decide(snapshot, request)
	for _, target := range plan.Targets {
		isContainer := planned.Kind == domain.KindContainer
		if isContainer && target.Container.Same(planned.Container) || !isContainer && target.Root.Same(planned.Root) {
			return target, snapshot, domain.Outcome{}
		}
	}
	return domain.StopTarget{}, snapshot, refused(changeRefusal(plan, planned))
}

// changeRefusal keeps a specific new reason, otherwise the target changed.
func changeRefusal(plan domain.StopPlan, planned domain.StopTarget) domain.Refusal {
	named := endpointsOf(planned)
	for _, result := range plan.Results {
		isNamed := slices.Contains(named, result.Endpoint)
		if isNamed && result.Target < 0 && result.Refusal.Code != domain.CodeNotListening {
			return result.Refusal
		}
	}
	return domain.Refusal{Code: domain.CodeChanged}
}

// attempt is one revalidated stop, right before it is delivered.
type attempt struct {
	planned  domain.StopTarget
	current  domain.StopTarget
	snapshot domain.Snapshot
	method   domain.Method
}

// deliver signals the root, then in a forced family whoever still holds the socket.
func (s *Stopper) deliver(ctx context.Context, a attempt, pins map[domain.ProcessID]pin) domain.Delivery {
	delivery := s.signal(ctx, a, a.current.Root, pins[a.planned.Root])
	if !delivery.Accepted || a.method != domain.Forced {
		return delivery
	}
	for _, member := range a.current.Members {
		pinned := pins[member]
		if member == a.current.Root || pinned == nil {
			continue
		}
		next := s.signal(ctx, a, member, pinned)
		delivery.Killed = append(delivery.Killed, next.Killed...)
	}
	return delivery
}

// signal rereads identity and socket, checks the pin, and acts at once.
func (s *Stopper) signal(ctx context.Context, a attempt, id domain.ProcessID, pinned pin) domain.Delivery {
	approval, err := domain.Approve(a.planned, a.current, a.method)
	if err != nil {
		return domain.Delivery{Code: domain.AsError(err).Code}
	}
	if ctx.Err() != nil {
		return domain.Delivery{Code: domain.CodeCancelled}
	}
	if !s.stillHolds(a, id) || !pinned.Alive() {
		return domain.Delivery{Code: domain.CodeChanged}
	}
	if a.method == domain.Forced {
		return pinned.ForceStop(approval)
	}
	return pinned.Terminate(approval)
}

// stillHolds needs every named socket for a graceful stop, a planned one for a forced one.
func (s *Stopper) stillHolds(a attempt, id domain.ProcessID) bool {
	held := 0
	for _, socket := range a.current.Named {
		holder := domain.Holder{PID: id.PID, FD: descriptorOf(a.snapshot, id.PID, socket)}
		process, holds, err := s.world.Reread(holder, socket)
		isSame := err == nil && process.Same(id) && !process.Zombie && process.Tracer == domain.No
		gainedSuspend := process.Suspended && !a.planned.Suspended && a.method == domain.Graceful
		if !isSame || gainedSuspend {
			return false
		}
		if holds && slices.ContainsFunc(a.planned.Named, socket.Same) {
			held++
		}
	}
	if a.method == domain.Forced {
		return held > 0
	}
	return held > 0 && held == len(a.current.Named)
}

// descriptorOf recalls which descriptor holds a socket, -1 when unknown.
func descriptorOf(snapshot domain.Snapshot, pid int, id domain.SocketID) int {
	for _, socket := range snapshot.On(id.Endpoint) {
		for _, holder := range socket.Holders {
			if socket.ID.Same(id) && holder.PID == pid {
				return holder.FD
			}
		}
	}
	return -1
}

// endpointsOf lists the named endpoints of a target.
func endpointsOf(target domain.StopTarget) []domain.Endpoint {
	var endpoints []domain.Endpoint
	for _, socket := range target.Named {
		endpoints = append(endpoints, socket.Endpoint)
	}
	for _, binding := range target.Bindings {
		endpoints = append(endpoints, binding.Endpoint())
	}
	slices.SortFunc(endpoints, domain.Endpoint.Compare)
	return slices.Compact(endpoints)
}

// refused is an outcome where nothing was sent.
func refused(refusal domain.Refusal) domain.Outcome {
	return domain.Outcome{Code: refusal.Code, Advice: refusal.Advice}
}

// refusedFor turns an executor error into an outcome.
func refusedFor(err error) domain.Outcome {
	return refused(domain.Refusal{Code: domain.AsError(err).Code})
}
