package stopper

import (
	"errors"
	"fmt"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

const stateZombie = 5

var errGone = domain.NewError(domain.CodeChanged, "The process is gone", "Run portly again to see what is listening")

// processPin is only a PID, because macOS has no stable handle.
type processPin struct{ id domain.ProcessID }

// pinProcess remembers an identity. Alive compares it again.
func pinProcess(id domain.ProcessID) (pin, error) {
	pinned := &processPin{id: id}
	if id.PID <= 1 || !pinned.Alive() {
		return nil, errGone
	}
	return pinned, nil
}

// Alive requires the same start time, which a reused PID can't have.
func (p *processPin) Alive() bool {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", p.id.PID)
	if err != nil || info.Proc.P_stat == stateZombie {
		return false
	}
	return p.id.Start == fmt.Sprintf("%d.%06d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec)
}

// Terminate asks the approved root to stop, and wakes it when suspended.
func (p *processPin) Terminate(approval *domain.Approval) domain.Delivery {
	target, err := approval.Take(domain.Graceful)
	if err != nil || target.Root != p.id {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	delivery := p.send(unix.SIGTERM)
	if delivery.Accepted && target.Suspended {
		delivery.Resumed = p.send(unix.SIGCONT).Accepted
	}
	return delivery
}

// ForceStop kills one approved member.
func (p *processPin) ForceStop(approval *domain.Approval) domain.Delivery {
	target, err := approval.Take(domain.Forced)
	if err != nil || !slices.Contains(target.Members, p.id) {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	delivery := p.send(unix.SIGKILL)
	if delivery.Accepted {
		delivery.Killed = []int{p.id.PID}
	}
	return delivery
}

// send signals one PID. Never a group, never everything.
func (p *processPin) send(signal unix.Signal) domain.Delivery {
	if p.id.PID <= 1 {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	err := unix.Kill(p.id.PID, signal)
	switch {
	case err == nil:
		return domain.Delivery{Accepted: true}
	case errors.Is(err, unix.ESRCH):
		return domain.Delivery{Code: domain.CodeChanged}
	case errors.Is(err, unix.EPERM):
		return domain.Delivery{Code: domain.CodePermissionDenied}
	}
	return domain.Delivery{Code: domain.CodeInternal}
}

// Close has nothing to release.
func (p *processPin) Close() {}
