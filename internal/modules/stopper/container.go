package stopper

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// resolvePublished allows one container that explains every socket.
func resolvePublished(snapshot domain.Snapshot, result resolution, sockets []domain.Socket, publishers []domain.Publisher, isForced bool) resolution {
	if len(publishers) > 1 {
		return result.refuse(domain.CodeAmbiguous)
	}
	owner := publishers[0]
	isLocal := snapshot.ProvenLocal(owner.Container)
	// A closed engine of the same user can't own what a proven container publishes
	isUnclear := hasUnansweredEngine(snapshot, owner.Engine.OwnerUID) || !isLocal && hasSilentEngine(snapshot, owner.Engine.OwnerUID)
	if isUnclear {
		return result.refuse(domain.CodeEngineUnresponsive)
	}
	bindings := owner.Container.Publishes(result.endpoint)
	for _, socket := range sockets {
		if !isExplained(snapshot, owner.Engine, bindings, socket, isLocal) {
			return result.refuse(domain.CodeAmbiguous)
		}
	}
	if !isLocal && len(sockets) == 0 {
		return result.refuse(domain.CodeContainerUnproven)
	}
	target := containerTarget(owner.Container)
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
			if !everyInside(snapshot, result.owners, container) {
				return result.refuse(domain.CodeContainerUnproven)
			}
			target := containerTarget(container)
			for _, socket := range sockets {
				target.Named = append(target.Named, socket.ID)
			}
			return finishContainer(snapshot, result, domain.Publisher{Engine: engine, Container: container}, target, isForced)
		}
	}
	return result.refuse(domain.CodeContainerUnproven)
}

// everyInside ties each holder to the container, because anyone may name a cgroup.
func everyInside(snapshot domain.Snapshot, pids []int, container domain.Container) bool {
	return !slices.ContainsFunc(pids, func(pid int) bool { return !snapshot.IsInside(pid, container) })
}

// holdsFromContainer reports whether any holder runs in a container.
func holdsFromContainer(snapshot domain.Snapshot, pids []int) bool {
	return slices.ContainsFunc(pids, func(pid int) bool { return len(snapshot.Processes[pid].ContainerIDs) > 0 })
}

// finishContainer applies the rules shared by every container target.
func finishContainer(snapshot domain.Snapshot, result resolution, owner domain.Publisher, target domain.StopTarget, isForced bool) resolution {
	container, host := owner.Container, snapshot.Host
	isSelf := host.SelfContainer != "" && container.ID == host.SelfContainer
	isSelfUnclear := host.SelfContainer == "" && host.InContainer != domain.No
	switch {
	case isSelf || isSelfUnclear:
		return result.refuse(domain.CodeContainerSelf)
	case snapshot.EnginesSeen != domain.Complete || owner.Engine.Answer != domain.AnswerOK:
		return result.refuse(domain.CodeEngineUnresponsive)
	case !container.Inspected || !container.Ref().Same(container.Ref()):
		return result.refuse(domain.CodeEngineUnresponsive)
	case container.Orchestrator != "":
		return result.refuse(domain.CodeContainerManaged)
	case !target.GracefulAvailable && !isForced:
		return result.refuse(domain.CodeNoGraceful)
	}
	result.candidate = &candidate{target: target, engine: owner.Engine.Kind, container: container}
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
		other := domain.AddressPort{Endpoint: binding.Endpoint(), Addr: binding.Address()}
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

// isExplained requires the engine's user, its program and an exact binding.
func isExplained(snapshot domain.Snapshot, engine domain.Engine, bindings []domain.Binding, socket domain.Socket, isLocal bool) bool {
	if engine.OwnerUID == "" || socket.CreatorUID != engine.OwnerUID || !slices.ContainsFunc(bindings, func(b domain.Binding) bool { return b.Matches(socket.ID) }) {
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
