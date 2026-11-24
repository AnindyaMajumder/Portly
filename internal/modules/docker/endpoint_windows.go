package docker

import (
	"context"
	"errors"
	"net"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

var errUnsupported = errors.New("named pipes are not supported yet")

// knownPaths lists the default pipes.
func (environment) knownPaths() []string {
	return []string{`\\.\pipe\docker_engine`, `\\.\pipe\dockerDesktopLinuxEngine`, `\\.\pipe\podman-machine-default`}
}

// openEndpoint reports every pipe as unobserved, which blocks stops.
func openEndpoint(string, int) (endpoint, domain.Answer) { return endpoint{}, domain.AnswerUnobserved }

// dial always fails.
func (endpoint) dial(context.Context) (net.Conn, error) { return nil, errUnsupported }

// answerFor can't tell errors apart yet.
func answerFor(error) domain.Answer { return domain.AnswerUnobserved }
