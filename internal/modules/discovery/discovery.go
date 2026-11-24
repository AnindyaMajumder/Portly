// Package discovery finds sockets and the processes behind them.
package discovery

import (
	"context"
	"maps"
	"slices"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Scanner reads the operating system's socket and process tables.
type Scanner struct{ source source }

// New returns a scanner for this machine.
func New() *Scanner { return &Scanner{source: newSource()} }

// Scan takes one snapshot of everything visible. Partial results are normal.
func (s *Scanner) Scan(ctx context.Context) (domain.Snapshot, error) {
	return s.source.scan(ctx)
}

// Observe takes a snapshot for a stop and says whether the endpoints were stable.
func (s *Scanner) Observe(ctx context.Context, endpoints []domain.Endpoint) (domain.Snapshot, error) {
	snapshot, err := observe(ctx, s.source.scan, endpoints)
	if err != nil {
		return domain.Snapshot{}, err
	}
	s.source.addManagers(ctx, &snapshot, endpoints)
	return snapshot, nil
}

// Reread reads one process again and whether it still holds the socket.
func (s *Scanner) Reread(holder domain.Holder, id domain.SocketID) (domain.Process, bool, error) {
	return s.source.reread(holder, id)
}

type scanFunc func(context.Context) (domain.Snapshot, error)

// observe scans twice, and once more when the endpoints changed in between.
func observe(ctx context.Context, scan scanFunc, endpoints []domain.Endpoint) (domain.Snapshot, error) {
	previous, err := scan(ctx)
	if err != nil {
		return domain.Snapshot{}, err
	}
	for range 2 {
		current, err := scan(ctx)
		if err != nil {
			return domain.Snapshot{}, err
		}
		stable := sameOwners(previous, current, endpoints)
		markStable(&current, endpoints, stable)
		previous = current
		if stable {
			break
		}
	}
	return previous, nil
}

// markStable records stability without touching other endpoints.
func markStable(snapshot *domain.Snapshot, endpoints []domain.Endpoint, stable bool) {
	ports := maps.Clone(snapshot.Ports)
	if ports == nil {
		ports = map[domain.Endpoint]domain.PortState{}
	}
	for _, endpoint := range endpoints {
		state := snapshot.PortState(endpoint)
		state.Stable = domain.TriOf(stable)
		ports[endpoint] = state
	}
	snapshot.Ports = ports
}

// sameOwners compares sockets and their holders on the named endpoints.
func sameOwners(a, b domain.Snapshot, endpoints []domain.Endpoint) bool {
	for _, endpoint := range endpoints {
		if !maps.EqualFunc(holdersByID(a, endpoint), holdersByID(b, endpoint), slices.Equal) {
			return false
		}
	}
	return true
}

// holdersByID maps each socket on an endpoint to its holders' identities.
func holdersByID(snapshot domain.Snapshot, endpoint domain.Endpoint) map[domain.SocketID][]domain.ProcessID {
	found := map[domain.SocketID][]domain.ProcessID{}
	for _, socket := range snapshot.On(endpoint) {
		ids := found[socket.ID]
		for _, pid := range domain.HolderPIDs([]domain.Socket{socket}) {
			ids = append(ids, snapshot.Processes[pid].ProcessID)
		}
		found[socket.ID] = ids
	}
	return found
}
