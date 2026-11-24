package stopper

import (
	"errors"
	"slices"
	"strconv"

	"golang.org/x/sys/windows"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

const (
	systemPID       = 4
	forcedExitCode  = 1
	stillActiveWait = uint32(windows.WAIT_TIMEOUT)
)

var errGone = domain.NewError(domain.CodeChanged, "The process is gone", "Run portly again to see what is listening")

// processPin is an open handle, which keeps the PID from being reused.
type processPin struct {
	id     domain.ProcessID
	handle windows.Handle
}

// pinProcess opens a handle and checks it is the confirmed process.
func pinProcess(id domain.ProcessID) (pin, error) {
	if id.PID <= systemPID {
		return nil, errGone
	}
	access := uint32(windows.PROCESS_TERMINATE | windows.SYNCHRONIZE | windows.PROCESS_QUERY_LIMITED_INFORMATION)
	handle, err := windows.OpenProcess(access, false, uint32(id.PID))
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, domain.NewError(domain.CodePermissionDenied, "The process belongs to another user", "Use an administrator terminal")
	}
	if err != nil {
		return nil, errGone
	}
	pinned := &processPin{id: id, handle: handle}
	if !pinned.isConfirmed() {
		pinned.Close()
		return nil, errGone
	}
	return pinned, nil
}

// isConfirmed compares the creation time of the handle itself.
func (p *processPin) isConfirmed() bool {
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(p.handle, &created, &exited, &kernel, &user) != nil {
		return false
	}
	started := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return started != 0 && p.id.Start == strconv.FormatUint(started, 10)
}

// Alive waits zero time on the handle.
func (p *processPin) Alive() bool {
	result, err := windows.WaitForSingleObject(p.handle, 0)
	return err == nil && result == stillActiveWait
}

// Terminate refuses, because Windows has no safe graceful stop.
func (p *processPin) Terminate(approval *domain.Approval) domain.Delivery {
	_, _ = approval.Take(domain.Graceful)
	return domain.Delivery{Code: domain.CodeNoGraceful}
}

// ForceStop ends one approved member through its pinned handle.
func (p *processPin) ForceStop(approval *domain.Approval) domain.Delivery {
	target, err := approval.Take(domain.Forced)
	if err != nil || !slices.Contains(target.Members, p.id) {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	err = windows.TerminateProcess(p.handle, forcedExitCode)
	switch {
	case err == nil:
		return domain.Delivery{Accepted: true, Killed: []int{p.id.PID}}
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return domain.Delivery{Code: domain.CodePermissionDenied}
	}
	return domain.Delivery{Code: domain.CodeInternal}
}

// Close releases the handle.
func (p *processPin) Close() { _ = windows.CloseHandle(p.handle) }
