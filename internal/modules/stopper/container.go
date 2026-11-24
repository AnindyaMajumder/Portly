package stopper

import (
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// publisher is a container and the engine that reported it.
type publisher struct {
	engine    domain.Engine
	container domain.Container
}

// publishersOf lists running containers publishing an endpoint, each once.
func publishersOf(snapshot domain.Snapshot, endpoint domain.Endpoint) []publisher {
	var found []publisher
	for _, engine := range snapshot.Engines {
		for _, container := range engine.Containers {
			isKnown := slices.ContainsFunc(found, func(p publisher) bool { return p.container.ID == container.ID })
			if container.Running && len(container.Publishes(endpoint)) > 0 && !isKnown {
				container.Engine = engine.ID
				found = append(found, publisher{engine, container})
			}
		}
	}
	return found
}

// resolvePublished allows one container that explains every socket.
func resolvePublished(snapshot domain.Snapshot, result resolution, sockets []domain.Socket, publishers []publisher, isForced bool) resolution {
	if len(publishers) > 1 {
		return result.refuse(domain.CodeAmbiguous)
	}
	owner := publishers[0]
	if hasSilentEngine(snapshot, owner.engine.OwnerUID) {
		return result.refuse(domain.CodeEngineUnresponsive)
	}
	isLocal := isProvenLocal(snapshot, owner.container)
	bindings := owner.container.Publishes(result.endpoint)
	for _, socket := range sockets {
		if !isExplained(snapshot, owner.engine, bindings, socket, isLocal) {
			return result.refuse(domain.CodeAmbiguous)
		}
	}
	if !isLocal && len(sockets) == 0 {
		return result.refuse(domain.CodeContainerUnproven)
	}
	target := containerTarget(owner.container)
	target.Bindings = bindings
	return finishContainer(snapshot, result, owner, target, isForced)
}

// resolveHostNetwork maps host-network containers through their cgroup.
func resolveHostNetwork(snapshot domain.Snapshot, result resolution, sockets []domain.Socket, isForced bool) resolution {
	var id string
	for _, pid := range result.owners {
		ids := snapshot.Processes[pid].ContainerIDs
		if len(ids) == 0 || id != "" && ids[0] != id {
			return result.refuse(domain.CodeAmbiguous)
		}
		id = ids[0]
	}
	for _, engine := range snapshot.Engines {
		for _, container := range engine.Containers {
			if container.ID != id || !container.Running || !container.HostNet {
				continue
			}
			container.Engine = engine.ID
			result.containers = append(result.containers, container.Ref())
			if !isProvenLocal(snapshot, container) {
				return result.refuse(domain.CodeContainerUnproven)
			}
			target := containerTarget(container)
			for _, socket := range sockets {
				target.Named = append(target.Named, socket.ID)
			}
			return finishContainer(snapshot, result, publisher{engine, container}, target, isForced)
		}
	}
	return result.refuse(domain.CodeContainerUnproven)
}

// holdsFromContainer reports whether any holder runs in a container.
func holdsFromContainer(snapshot domain.Snapshot, pids []int) bool {
	return slices.ContainsFunc(pids, func(pid int) bool { return len(snapshot.Processes[pid].ContainerIDs) > 0 })
}

// finishContainer applies the rules shared by every container target.
func finishContainer(snapshot domain.Snapshot, result resolution, owner publisher, target domain.StopTarget, isForced bool) resolution {
	container, host := owner.container, snapshot.Host
	isSelf := host.SelfContainer != "" && container.ID == host.SelfContainer
	isSelfUnclear := host.SelfContainer == "" && host.InContainer != domain.No
	switch {
	case isSelf || isSelfUnclear:
		return result.refuse(domain.CodeContainerSelf)
	case snapshot.EnginesSeen != domain.Complete || owner.engine.Answer != domain.AnswerOK:
		return result.refuse(domain.CodeEngineUnresponsive)
	case !container.Inspected || !container.Ref().Same(container.Ref()):
		return result.refuse(domain.CodeEngineUnresponsive)
	case container.Orchestrator != "":
		return result.refuse(domain.CodeContainerManaged)
	case !target.GracefulAvailable && !isForced:
		return result.refuse(domain.CodeNoGraceful)
	}
	result.candidate = &candidate{target: target, engine: owner.engine.Kind, container: container}
	return result
}

// containerTarget describes a container and what else it publishes.
func containerTarget(container domain.Container) domain.StopTarget {
	target := domain.StopTarget{
		Kind: domain.KindContainer, Container: container.Ref(), SharesNet: container.SharesNet,
		Paused: container.Paused, AutoRemove: container.AutoRemove,
		GracefulAvailable: hasGracefulSignal(container.StopSignal),
	}
	for _, binding := range container.Ports {
		address := binding.HostAddr
		if !address.IsValid() {
			address = netip.IPv4Unspecified()
		}
		other := domain.AddressPort{Endpoint: binding.Endpoint(), Addr: address}
		if !slices.Contains(target.Other, other) {
			target.Other = append(target.Other, other)
		}
	}
	domain.SortAddresses(target.Other)
	return target
}

// Anything else is treated as unable to stop gracefully
var gracefulSignals = []string{"", "TERM", "INT", "QUIT", "HUP", "USR1", "USR2", "WINCH"}

// HUP, INT, QUIT and TERM have the same number on every CPU
var gracefulNumbers = []int{1, 2, 3, 15}

// hasGracefulSignal accepts only known signals, because engines parse numbers loosely.
func hasGracefulSignal(signal string) bool {
	name := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(signal)), "SIG")
	if number, err := strconv.Atoi(name); err == nil {
		return slices.Contains(gracefulNumbers, number)
	}
	return slices.Contains(gracefulSignals, name) || realTimeSignal.MatchString(name)
}

// systemd images use SIGRTMIN+3
var realTimeSignal = regexp.MustCompile(`^RT(MIN(\+[0-9]{1,2})?|MAX(-[0-9]{1,2})?)$`)

// isProvenLocal requires the init process here, inside that container's cgroup.
func isProvenLocal(snapshot domain.Snapshot, container domain.Container) bool {
	init, known := snapshot.Processes[container.InitPID]
	return known && container.InitPID > 1 && container.ID != "" && slices.Contains(init.ContainerIDs, container.ID)
}

// isExplained requires the engine's user, its program and an exact binding.
func isExplained(snapshot domain.Snapshot, engine domain.Engine, bindings []domain.Binding, socket domain.Socket, isLocal bool) bool {
	if engine.OwnerUID == "" || socket.CreatorUID != engine.OwnerUID || !matchesBinding(bindings, socket) {
		return false
	}
	if len(socket.Holders) == 0 {
		return isLocal
	}
	for _, holder := range socket.Holders {
		process, known := snapshot.Processes[holder.PID]
		if !known || process.UserID != engine.OwnerUID || !matchesAny(programNames(process), holderNames(engine, isLocal)) {
			return false
		}
	}
	return true
}

// matchesBinding compares addresses exactly. Bindings already match the port.
func matchesBinding(bindings []domain.Binding, socket domain.Socket) bool {
	address := socket.ID.Addr.WithZone("")
	for _, binding := range bindings {
		isAnyAddress := !binding.HostAddr.IsValid() && address.IsUnspecified()
		if isAnyAddress || binding.HostAddr == address {
			return true
		}
	}
	return false
}
