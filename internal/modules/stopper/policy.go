// Package stopper decides what may be stopped, and stops it.
package stopper

import (
	"slices"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// gates says which evidence has been accepted on this platform.
type gates struct {
	native   bool
	docker   bool
	podman   bool
	graceful bool
	families bool
	// Evidence builds only act on what their test started
	guard func(domain.Snapshot, candidate) bool
}

// candidate is a target that passed every rule before the gate.
type candidate struct {
	target    domain.StopTarget
	engine    string
	container domain.Container
}

// resolution is the decision for one endpoint.
type resolution struct {
	endpoint   domain.Endpoint
	candidate  *candidate
	refusal    domain.Refusal
	owners     []int
	containers []domain.ContainerRef
}

// refuse ends a resolution with a reason.
func (r resolution) refuse(code domain.Code) resolution {
	return r.refuseWith(domain.Refusal{Code: code})
}

// refuseWith ends a resolution with a reason and advice.
func (r resolution) refuseWith(refusal domain.Refusal) resolution {
	r.candidate, r.refusal = nil, refusal
	return r
}

// decide is pure: the same snapshot and request give the same plan.
func decide(snapshot domain.Snapshot, request domain.StopRequest, gates gates) domain.StopPlan {
	var resolutions []resolution
	for _, endpoint := range distinct(request.Endpoints) {
		resolutions = append(resolutions, resolve(snapshot, endpoint, request.Force, gates))
	}
	resolutions = refuseTainted(snapshot, resolutions)
	for i, resolution := range resolutions {
		if resolution.candidate != nil && !isGateOpen(snapshot, *resolution.candidate, gates) {
			resolutions[i] = resolution.refuse(domain.CodePlatform)
		}
	}
	return buildPlan(snapshot, resolutions)
}

// distinct drops repeated endpoints and keeps the user's order.
func distinct(endpoints []domain.Endpoint) []domain.Endpoint {
	var kept []domain.Endpoint
	for _, endpoint := range endpoints {
		if !slices.Contains(kept, endpoint) {
			kept = append(kept, endpoint)
		}
	}
	return kept
}

// resolve finds the one owner of an endpoint, or the reason to refuse.
func resolve(snapshot domain.Snapshot, endpoint domain.Endpoint, isForced bool, gates gates) resolution {
	sockets, publishers := snapshot.On(endpoint), snapshot.Publishers(endpoint)
	result := resolution{endpoint: endpoint, owners: domain.HolderPIDs(sockets)}
	for _, publisher := range publishers {
		result.containers = append(result.containers, publisher.Container.Ref())
	}
	state := snapshot.PortState(endpoint)
	if state.Table != domain.Complete {
		return result.refuse(domain.CodeEvidenceUnavailable)
	}
	if len(sockets) == 0 && len(publishers) == 0 {
		return result.refuseWith(notListening(snapshot, endpoint))
	}
	if state.Stable != domain.Yes {
		return result.refuse(domain.CodeEvidenceUnstable)
	}
	if snapshot.Host.ProcessTable != domain.Complete {
		return result.refuse(domain.CodeEvidenceUnavailable)
	}
	if len(publishers) > 0 {
		return resolvePublished(snapshot, result, sockets, publishers, isForced)
	}
	if code := ownershipProblem(snapshot, sockets); code != "" {
		return result.refuseWith(ownershipRefusal(snapshot, sockets, code))
	}
	if holdsFromContainer(snapshot, result.owners) {
		return resolveHostNetwork(snapshot, result, sockets, isForced)
	}
	return resolveNative(snapshot, result, sockets, isForced, gates)
}

// notListening mentions UDP, because a bare port never means UDP.
func notListening(snapshot domain.Snapshot, endpoint domain.Endpoint) domain.Refusal {
	udp := domain.Endpoint{Port: endpoint.Port, Protocol: domain.UDP}
	isUDPOnly := endpoint.Protocol == domain.TCP && len(snapshot.On(udp)) > 0
	return domain.Refusal{Code: domain.CodeNotListening, Advice: domain.Advice{UDPOnly: isUDPOnly}}
}

// refuseTainted refuses a possible owner of one refused port everywhere.
func refuseTainted(snapshot domain.Snapshot, resolutions []resolution) []resolution {
	for changed := true; changed; {
		changed = false
		owners, containers := refusedOwners(resolutions)
		for i, resolution := range resolutions {
			if resolution.candidate != nil && isTainted(snapshot, resolution.candidate.target, owners, containers) {
				resolutions[i] = resolution.refuse(domain.CodeRefusedElsewhere)
				changed = true
			}
		}
	}
	return resolutions
}

// refusedOwners collects every possible owner of a refused port.
func refusedOwners(resolutions []resolution) (map[int]bool, map[domain.ContainerRef]bool) {
	owners, containers := map[int]bool{}, map[domain.ContainerRef]bool{}
	for _, resolution := range resolutions {
		if resolution.candidate != nil || resolution.refusal.Code == domain.CodeNotListening {
			continue
		}
		for _, pid := range resolution.owners {
			owners[pid] = true
		}
		for _, container := range resolution.containers {
			containers[container] = true
		}
	}
	return owners, containers
}

// isTainted reports whether a target may own a refused port.
func isTainted(snapshot domain.Snapshot, target domain.StopTarget, owners map[int]bool, containers map[domain.ContainerRef]bool) bool {
	if target.Kind == domain.KindContainer {
		for pid := range owners {
			if slices.Contains(snapshot.Processes[pid].ContainerIDs, target.Container.ID) {
				return true
			}
		}
		return containers[target.Container]
	}
	if slices.ContainsFunc(target.Members, func(member domain.ProcessID) bool { return owners[member.PID] }) {
		return true
	}
	// Stopping a parent may take its refused child down
	return slices.ContainsFunc(snapshot.Descendants(target.Root.PID), func(pid int) bool { return owners[pid] })
}

// isGateOpen comes last, so specific refusals stay visible.
func isGateOpen(snapshot domain.Snapshot, candidate candidate, gates gates) bool {
	isOpen := gates.native
	if candidate.target.Kind == domain.KindContainer {
		isEngineOpen := candidate.engine == "docker" && gates.docker || candidate.engine == "podman" && gates.podman
		isOpen = gates.native && isEngineOpen
	}
	return isOpen && (gates.guard == nil || gates.guard(snapshot, candidate))
}

// buildPlan groups endpoints by target, so each is signalled once.
func buildPlan(snapshot domain.Snapshot, resolutions []resolution) domain.StopPlan {
	var plan domain.StopPlan
	for _, resolution := range resolutions {
		result := domain.PortResult{Endpoint: resolution.endpoint, Target: -1, Refusal: resolution.refusal}
		if resolution.candidate != nil {
			result.Target = mergeTarget(&plan, resolution.candidate.target)
		}
		plan.Results = append(plan.Results, result)
	}
	for i := range plan.Targets {
		addReach(snapshot, &plan.Targets[i])
	}
	plan.ID = planID(plan.Targets)
	return plan
}

// mergeTarget adds a target or joins it with the same one.
func mergeTarget(plan *domain.StopPlan, target domain.StopTarget) int {
	for i := range plan.Targets {
		known := &plan.Targets[i]
		isSame := known.Kind == domain.KindContainer && target.Kind == known.Kind && known.Container == target.Container ||
			known.Kind != domain.KindContainer && target.Kind != domain.KindContainer && known.Root == target.Root
		if !isSame {
			continue
		}
		if target.Kind == domain.KindFamily {
			known.Kind = domain.KindFamily
		}
		for _, member := range target.Members {
			if !slices.Contains(known.Members, member) {
				known.Members = append(known.Members, member)
			}
		}
		known.Named = append(known.Named, target.Named...)
		known.Bindings = append(known.Bindings, target.Bindings...)
		return i
	}
	plan.Targets = append(plan.Targets, target)
	return len(plan.Targets) - 1
}

// addReach lists every other endpoint and child the stop affects.
func addReach(snapshot domain.Snapshot, target *domain.StopTarget) {
	if target.Kind == domain.KindContainer {
		target.Other = slices.DeleteFunc(target.Other, func(other domain.AddressPort) bool {
			return slices.ContainsFunc(target.Bindings, func(named domain.Binding) bool { return named.Endpoint() == other.Endpoint })
		})
		addContainerReach(snapshot, target)
		return
	}
	affected := []int{target.Root.PID}
	for _, pid := range snapshot.Descendants(target.Root.PID) {
		affected = append(affected, pid)
		isMember := slices.ContainsFunc(target.Members, func(member domain.ProcessID) bool { return member.PID == pid })
		if !isMember && !snapshot.Processes[pid].Zombie {
			target.Children = append(target.Children, snapshot.Processes[pid].ProcessID)
		}
	}
	for _, pid := range affected {
		for _, socket := range snapshot.HeldBy(pid) {
			addOther(snapshot.Host, target, socket)
		}
	}
	domain.SortAddresses(target.Other)
}

// addContainerReach adds host-network sockets and processes of a container.
func addContainerReach(snapshot domain.Snapshot, target *domain.StopTarget) {
	isInside := func(pid int) bool {
		return slices.Contains(snapshot.Processes[pid].ContainerIDs, target.Container.ID)
	}
	for _, socket := range snapshot.Sockets {
		if slices.ContainsFunc(socket.Holders, func(holder domain.Holder) bool { return isInside(holder.PID) }) {
			addOther(snapshot.Host, target, socket)
		}
	}
	domain.SortAddresses(target.Other)
	for pid, process := range snapshot.Processes {
		if isInside(pid) && !process.Zombie {
			target.Children = append(target.Children, process.ProcessID)
		}
	}
	slices.SortFunc(target.Children, func(a, b domain.ProcessID) int { return a.PID - b.PID })
}

// addOther skips named sockets and UDP noise.
func addOther(host domain.Host, target *domain.StopTarget, socket domain.Socket) {
	isNoise := socket.ID.Protocol == domain.UDP && (socket.Connected || host.Ephemeral.Contains(socket.ID.Port))
	isNamed := slices.ContainsFunc(target.Named, func(id domain.SocketID) bool { return id.Endpoint == socket.ID.Endpoint })
	if !isNoise && !isNamed && !slices.Contains(target.Other, socket.ID.AddressPort) {
		target.Other = append(target.Other, socket.ID.AddressPort)
	}
}
