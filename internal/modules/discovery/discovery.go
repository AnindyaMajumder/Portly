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
		isStable := markStable(previous, &current, endpoints)
		previous = current
		if isStable {
			break
		}
	}
	return previous, nil
}

// markStable compares each endpoint on its own, so one can't block another.
func markStable(previous domain.Snapshot, snapshot *domain.Snapshot, endpoints []domain.Endpoint) bool {
	isEveryStable := true
	ports := maps.Clone(snapshot.Ports)
	if ports == nil {
		ports = map[domain.Endpoint]domain.PortState{}
	}
	for _, endpoint := range endpoints {
		isStable := maps.EqualFunc(holdersByID(previous, endpoint), holdersByID(*snapshot, endpoint), slices.Equal)
		isEveryStable = isEveryStable && isStable
		state := snapshot.PortState(endpoint)
		state.Stable = domain.TriOf(isStable)
		ports[endpoint] = state
	}
	snapshot.Ports = ports
	return isEveryStable
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
