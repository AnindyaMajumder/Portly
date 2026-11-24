package domain

import "slices"

// Parent chains longer than this are treated as broken
const maxAncestors = 128

// Host is what is true of this machine and caller.
type Host struct {
	SelfPID        int
	CallerUID      string
	Privileged     bool
	SystemUIDMax   uint32
	SelfContainer  string
	InContainer    Tri
	ServiceManager Tri
	ProcessTable   Completeness
	Ephemeral      Range
}

// PortState is how reliable the socket table is for one endpoint.
type PortState struct {
	Table  Completeness
	Stable Tri
}

// Snapshot is one view of sockets, processes and engines.
type Snapshot struct {
	Host        Host
	Ports       map[Endpoint]PortState
	Sockets     []Socket
	Processes   map[int]Process
	Engines     []Engine
	EnginesSeen Completeness
	Warnings    []string
}

// On returns the sockets on an endpoint.
func (s Snapshot) On(endpoint Endpoint) []Socket {
	var found []Socket
	for _, socket := range s.Sockets {
		if socket.ID.Endpoint == endpoint {
			found = append(found, socket)
		}
	}
	return found
}

// PortState falls back to the protocol's table when a port has no sockets.
func (s Snapshot) PortState(endpoint Endpoint) PortState {
	if state, known := s.Ports[endpoint]; known {
		return state
	}
	return s.Ports[Endpoint{Protocol: endpoint.Protocol}]
}

// HeldBy returns every socket a process holds.
func (s Snapshot) HeldBy(pid int) []Socket {
	var found []Socket
	for _, socket := range s.Sockets {
		if socket.HeldBy(pid) {
			found = append(found, socket)
		}
	}
	return found
}

// Ancestors walks parents, nearest first. False when the chain is broken.
func (s Snapshot) Ancestors(pid int) ([]Process, bool) {
	var chain []Process
	seen := map[int]bool{pid: true}
	current, known := s.Processes[pid]
	for known && current.PPID > 0 {
		parent, found := s.Processes[current.PPID]
		if !found || seen[parent.PID] || len(chain) == maxAncestors {
			return chain, false
		}
		seen[parent.PID] = true
		chain = append(chain, parent)
		current = parent
	}
	return chain, known
}

// Descendants returns every known child and grandchild, sorted.
func (s Snapshot) Descendants(pid int) []int {
	children := map[int][]int{}
	for _, process := range s.Processes {
		children[process.PPID] = append(children[process.PPID], process.PID)
	}
	seen := map[int]bool{pid: true}
	var found []int
	for queue := []int{pid}; len(queue) > 0; queue = queue[1:] {
		for _, child := range children[queue[0]] {
			if !seen[child] {
				seen[child] = true
				found = append(found, child)
				queue = append(queue, child)
			}
		}
	}
	slices.Sort(found)
	return found
}

// HolderPIDs lists distinct holders, sorted.
func HolderPIDs(sockets []Socket) []int {
	var pids []int
	for _, socket := range sockets {
		for _, holder := range socket.Holders {
			pids = append(pids, holder.PID)
		}
	}
	slices.Sort(pids)
	return slices.Compact(pids)
}
