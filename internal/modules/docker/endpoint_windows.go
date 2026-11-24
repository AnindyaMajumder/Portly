package docker

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"golang.org/x/sys/windows"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

const pipeRetry = 20 * time.Millisecond

// knownPaths lists the default pipes.
func (environment) knownPaths() []string {
	return []string{`\\.\pipe\docker_engine`, `\\.\pipe\dockerDesktopLinuxEngine`, `\\.\pipe\podman-machine-default`}
}

// openEndpoint accepts a pipe only from the system, administrators or the caller.
func openEndpoint(path string, _ int) (endpoint, domain.Answer) {
	if !isLocalPipe(path) {
		return endpoint{}, domain.AnswerAbsent
	}
	file, err := openPipe(path)
	if errors.Is(err, windows.ERROR_PIPE_BUSY) {
		return endpoint{}, domain.AnswerTimeout
	}
	if err != nil {
		return endpoint{}, answerFor(err)
	}
	defer file.Close()
	owner, err := ownerOf(file)
	if err != nil {
		return endpoint{}, domain.AnswerUnobserved
	}
	if !isTrustedOwner(owner) {
		return endpoint{}, domain.AnswerAbsent
	}
	return endpoint{path: path, ownerUID: owner.String()}, domain.AnswerOK
}

// ownerOf reads the pipe's owner without leaving overlapped mode.
func ownerOf(file *os.File) (*windows.SID, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	var owner *windows.SID
	var failure error
	err = raw.Control(func(handle uintptr) {
		descriptor, err := windows.GetSecurityInfo(windows.Handle(handle), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if err != nil {
			failure = err
			return
		}
		owner, _, failure = descriptor.Owner()
	})
	return owner, errors.Join(err, failure)
}

// isTrustedOwner matches the system, administrators and the caller.
func isTrustedOwner(owner *windows.SID) bool {
	if owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return true
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && owner.Equals(user.User.Sid)
}

// openPipe never lets the server impersonate the caller.
func openPipe(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.FILE_FLAG_OVERLAPPED | windows.SECURITY_SQOS_PRESENT | windows.SECURITY_IDENTIFICATION)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

// pipeAddress names a pipe.
type pipeAddress string

// Network is always pipe.
func (pipeAddress) Network() string { return "pipe" }

// String is the pipe's path.
func (p pipeAddress) String() string { return string(p) }

// pipeConn lets an overlapped pipe serve as a connection.
type pipeConn struct {
	*os.File
	address pipeAddress
}

// LocalAddr is the pipe.
func (p pipeConn) LocalAddr() net.Addr { return p.address }

// RemoteAddr is the pipe.
func (p pipeConn) RemoteAddr() net.Addr { return p.address }

// dial opens the pipe, waiting while every instance is busy.
func (e endpoint) dial(ctx context.Context) (net.Conn, error) {
	for {
		file, err := openPipe(e.path)
		if err == nil {
			return e.verified(file)
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pipeRetry):
		}
	}
}

// verified checks this very connection, because the pipe may have been replaced.
func (e endpoint) verified(file *os.File) (net.Conn, error) {
	owner, err := ownerOf(file)
	if err != nil || owner.String() != e.ownerUID {
		_ = file.Close()
		return nil, os.ErrPermission
	}
	return pipeConn{file, pipeAddress(e.path)}, nil
}

// answerFor sorts connection errors by what they mean for a stop.
func answerFor(err error) domain.Answer {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return domain.AnswerAbsent
	case errors.Is(err, os.ErrPermission):
		return domain.AnswerDenied
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.Canceled):
		return domain.AnswerTimeout
	}
	return domain.AnswerMalformed
}
