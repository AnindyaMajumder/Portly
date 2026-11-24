package stopper

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// No pidfd on kernels before 5.3
const noPidfd = -1

var errNoPin = domain.NewError(domain.CodeEvidenceUnavailable, "The process could not be pinned", "Close some programs and try again")

var errGone = domain.NewError(domain.CodeChanged, "The process is gone", "Run portly again to see what is listening")

// processPin refers to one process, even if its PID is reused.
type processPin struct {
	id domain.ProcessID
	fd int
}

// pinProcess opens a pidfd, or falls back to the PID on old kernels.
func pinProcess(id domain.ProcessID) (pin, error) {
	if id.PID <= 1 {
		return nil, errGone
	}
	fd, err := unix.PidfdOpen(id.PID, 0)
	switch {
	case err == nil:
		return &processPin{id: id, fd: fd}, nil
	case errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM):
		return &processPin{id: id, fd: noPidfd}, nil
	case errors.Is(err, unix.ESRCH):
		return nil, errGone
	}
	return nil, errNoPin
}

// Alive polls the pidfd, because signal 0 also succeeds on zombies.
func (p *processPin) Alive() bool {
	if p.fd == noPidfd {
		return p.isSameRunning()
	}
	polled := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
	for {
		count, err := unix.Poll(polled, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err == nil && count == 0
	}
}

// isSameRunning compares start time and state, for kernels without a pidfd.
func (p *processPin) isSameRunning() bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.id.PID))
	closing := bytes.LastIndexByte(data, ')')
	if err != nil || closing < 0 {
		return false
	}
	fields := strings.Fields(string(data[closing+1:]))
	const state, start = 0, 19
	return len(fields) > start && fields[state] != "Z" && fields[state] != "X" && fields[start] == p.id.Start
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

// send signals through the pin. Never a group, never everything.
func (p *processPin) send(signal unix.Signal) domain.Delivery {
	if p.id.PID <= 1 {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	var err error
	if p.fd == noPidfd {
		err = unix.Kill(p.id.PID, signal)
	} else {
		err = unix.PidfdSendSignal(p.fd, signal, nil, 0)
	}
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

// Close releases the pidfd.
func (p *processPin) Close() {
	if p.fd != noPidfd {
		_ = unix.Close(p.fd)
	}
}
