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

// Publisher is a container and the engine that reported it.
type Publisher struct {
	Engine    Engine
	Container Container
}

// Publishers lists running containers publishing an endpoint, each once.
func (s Snapshot) Publishers(endpoint Endpoint) []Publisher {
	var found []Publisher
	for _, engine := range s.Engines {
		for _, container := range engine.Containers {
			isKnown := slices.ContainsFunc(found, func(p Publisher) bool { return p.Container.ID == container.ID })
			if container.Running && len(container.Publishes(endpoint)) > 0 && !isKnown {
				container.Engine = engine.ID
				found = append(found, Publisher{engine, container})
			}
		}
	}
	return found
}

// IsInside requires a process in the same cgroup subtree as the container's init process.
func (s Snapshot) IsInside(pid int, container Container) bool {
	path := s.containerPath(container)
	return path != "" && s.Processes[pid].ContainerPath == path
}

// ProvenLocal requires the init process here, inside that container's cgroup.
func (s Snapshot) ProvenLocal(container Container) bool {
	return s.containerPath(container) != ""
}

// containerPath is the cgroup path of a container's init process, empty when unproven.
func (s Snapshot) containerPath(container Container) string {
	init, known := s.Processes[container.InitPID]
	if !known || container.InitPID <= 1 || container.ID == "" || !slices.Contains(init.ContainerIDs, container.ID) {
		return ""
	}
	return init.ContainerPath
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
