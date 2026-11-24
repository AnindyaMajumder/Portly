//go:build unix

package docker

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// knownPaths lists default sockets of every supported engine.
func (e environment) knownPaths() []string {
	paths := []string{filepath.Join(e.root, "var/run/docker.sock")}
	if e.runtimeDir != "" {
		paths = append(paths, filepath.Join(e.runtimeDir, "docker.sock"), filepath.Join(e.runtimeDir, "podman/podman.sock"))
	}
	if e.home != "" {
		for _, name := range []string{".docker/run/docker.sock", ".docker/desktop/docker.sock", ".colima/default/docker.sock", ".orbstack/run/docker.sock"} {
			paths = append(paths, filepath.Join(e.home, name))
		}
	}
	return append(paths, filepath.Join(e.root, "run/podman/podman.sock"))
}

// openEndpoint resolves a socket and accepts it only from root or the caller.
func openEndpoint(path string, caller int) (endpoint, domain.Answer) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return endpoint{}, answerFor(err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return endpoint{}, answerFor(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != 0 && int(stat.Uid) != caller {
		return endpoint{}, domain.AnswerAbsent
	}
	return endpoint{path: resolved, ownerUID: strconv.FormatUint(uint64(stat.Uid), 10)}, domain.AnswerOK
}

// dial connects to the socket.
func (e endpoint) dial(ctx context.Context) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", e.path)
}

// answerFor sorts connection errors by what they mean for a stop.
func answerFor(err error) domain.Answer {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return domain.AnswerAbsent
	case errors.Is(err, syscall.ECONNREFUSED):
		return domain.AnswerRefused
	case errors.Is(err, os.ErrPermission):
		return domain.AnswerDenied
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.Canceled):
		return domain.AnswerTimeout
	}
	return domain.AnswerMalformed
}
