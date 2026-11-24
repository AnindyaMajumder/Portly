package stopper

import (
	"context"
	"slices"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// program tells a restarted server from another one.
type program struct {
	exe  string
	args []string
}

// programOf keeps what identifies a program across restarts.
func programOf(process domain.Process) program {
	return program{exe: process.Exe, args: process.Args}
}

// matches is false when the executable is unknown.
func (p program) matches(process domain.Process) bool {
	return p.exe != "" && p.exe == process.Exe && slices.Equal(p.args, process.Args)
}

// verifyProcess polls the process and the port until both are gone.
func (s *Stopper) verifyProcess(ctx context.Context, planned domain.StopTarget, program program, isAlive func() bool) domain.Outcome {
	return waitFor(ctx, processDeadline, func() (domain.Outcome, bool) {
		snapshot, err := s.world.Look(ctx)
		sockets, isReadable := socketsOn(snapshot, endpointsOf(planned))
		exited := domain.TriOf(!isAlive() && !hasLiveMember(snapshot, planned))
		if err != nil || !isReadable || snapshot.Host.ProcessTable != domain.Complete {
			return domain.Outcome{Code: domain.CodeUnknownOutcome, ProcessExited: exited}, false
		}
		outcome := domain.Outcome{ProcessExited: exited, PortReleased: domain.TriOf(len(sockets) == 0)}
		switch {
		case len(sockets) > 0:
			outcome.Code, outcome.HeldByTarget = classify(snapshot, planned, program, sockets)
		case exited != domain.Yes:
			outcome.Code = domain.CodeDraining
		}
		return outcome, outcome.Code == ""
	})
}

// hasLiveMember compares PID and start only, since exited holders lose details.
func hasLiveMember(snapshot domain.Snapshot, planned domain.StopTarget) bool {
	return slices.ContainsFunc(planned.Members, func(member domain.ProcessID) bool {
		process, known := snapshot.Processes[member.PID]
		return known && process.Start == member.Start && !process.Zombie
	})
}

// socketsOn lists sockets on endpoints, false when a table is not complete.
func socketsOn(snapshot domain.Snapshot, endpoints []domain.Endpoint) ([]domain.Socket, bool) {
	var sockets []domain.Socket
	for _, endpoint := range endpoints {
		if snapshot.PortState(endpoint).Table != domain.Complete {
			return nil, false
		}
		sockets = append(sockets, snapshot.On(endpoint)...)
	}
	return sockets, true
}

// classify says who holds the port now, and whether it is the target itself.
func classify(snapshot domain.Snapshot, planned domain.StopTarget, program program, sockets []domain.Socket) (domain.Code, bool) {
	isOriginal, isTarget := false, false
	for _, socket := range sockets {
		isOriginal = isOriginal || slices.ContainsFunc(planned.Named, socket.ID.Same)
		for _, holder := range socket.Holders {
			isTarget = isTarget || isMember(snapshot, planned, holder.PID)
		}
	}
	pids := domain.HolderPIDs(sockets)
	switch {
	case isOriginal || isTarget:
		return domain.CodeStillListening, isTarget
	case len(pids) == 0:
		return domain.CodeUnknownOutcome, false
	}
	isRestart := slices.ContainsFunc(pids, func(pid int) bool { return program.matches(snapshot.Processes[pid]) })
	if isRestart {
		return domain.CodeRestarted, false
	}
	return domain.CodeTakenOver, false
}

// isMember compares full identity, so a reused PID is not the target.
func isMember(snapshot domain.Snapshot, planned domain.StopTarget, pid int) bool {
	return slices.ContainsFunc(planned.Members, snapshot.Processes[pid].Same)
}
