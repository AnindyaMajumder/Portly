package discovery

import "github.com/AnindyaMajumder/Portly/internal/domain"

// portStates records table quality for every endpoint with a socket.
func portStates(sockets []domain.Socket, tables map[domain.Protocol]domain.Completeness) map[domain.Endpoint]domain.PortState {
	states := map[domain.Endpoint]domain.PortState{}
	for protocol, completeness := range tables {
		states[domain.Endpoint{Protocol: protocol}] = domain.PortState{Table: completeness}
	}
	for _, socket := range sockets {
		states[socket.ID.Endpoint] = domain.PortState{Table: tables[socket.ID.Protocol]}
	}
	return states
}
