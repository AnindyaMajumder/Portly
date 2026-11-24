//go:build !linux && !darwin

package discovery

import (
	"context"
	"errors"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

var errUnsupported = errors.New("this platform is not supported yet")

// source reports every call as unsupported until its platform lands.
type source struct{}

// newSource returns the unsupported source.
func newSource() source { return source{} }

// scan always fails.
func (source) scan(context.Context) (domain.Snapshot, error) {
	return domain.Snapshot{}, errUnsupported
}

// addManagers has nothing to add.
func (source) addManagers(context.Context, *domain.Snapshot, []domain.Endpoint) {}

// reread always fails.
func (source) reread(domain.Holder, domain.SocketID) (domain.Process, bool, error) {
	return domain.Process{}, false, errUnsupported
}
