package stopper

import (
	"regexp"
	"slices"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// resolveNative allows one process or one verified family.
func resolveNative(snapshot domain.Snapshot, result resolution, sockets []domain.Socket, isForced bool, gates gates) resolution {
	root, kind, ok := ownerOf(snapshot, sockets, result.owners, gates)
	if !ok {
		return result.refuse(domain.CodeAmbiguous)
	}
	target := domain.StopTarget{Kind: kind, Root: root.ProcessID, Suspended: root.Suspended, GracefulAvailable: gates.graceful}
	for _, pid := range result.owners {
		member := snapshot.Processes[pid]
		if refusal := processRefusal(snapshot, member, root); refusal.Code != "" {
			return result.refuseWith(refusal)
		}
		if !member.Same(member.ProcessID) || member.Zombie {
			return result.refuse(domain.CodeUnresolved)
		}
		target.Members = append(target.Members, member.ProcessID)
	}
	if code := descendantProblem(snapshot, root.PID); code != "" {
		return result.refuse(code)
	}
	if code := engineProblem(snapshot, root); code != "" {
		return result.refuse(code)
	}
	if !gates.graceful && !isForced {
		return result.refuse(domain.CodeNoGraceful)
	}
	for _, socket := range sockets {
		target.Named = append(target.Named, socket.ID)
	}
	target.Reloader = isReloaded(snapshot, root)
	result.candidate = &candidate{target: target}
	return result
}

// ownerOf returns the single holder, or the root of a verified family.
func ownerOf(snapshot domain.Snapshot, sockets []domain.Socket, pids []int, gates gates) (domain.Process, domain.TargetKind, bool) {
	if len(pids) == 1 {
		process, known := snapshot.Processes[pids[0]]
		return process, domain.KindProcess, known
	}
	if !gates.families {
		return domain.Process{}, "", false
	}
	for _, pid := range pids {
		root := snapshot.Processes[pid]
		if isFamilyRoot(snapshot, root, sockets, pids) {
			return root, domain.KindFamily, true
		}
	}
	return domain.Process{}, "", false
}

// isFamilyRoot requires same user, same program, a chain through holders and a handler.
func isFamilyRoot(snapshot domain.Snapshot, root domain.Process, sockets []domain.Socket, pids []int) bool {
	if root.Caught != domain.Yes || root.Exe == "" || root.UserID == "" {
		return false
	}
	for _, socket := range sockets {
		if !socket.HeldBy(root.PID) {
			return false
		}
	}
	for _, pid := range pids {
		member, known := snapshot.Processes[pid]
		isAlike := known && member.UserID == root.UserID && member.RealUID == root.RealUID && member.Exe == root.Exe
		if !isAlike || !descendsThroughHolders(snapshot, pid, root.PID, pids) {
			return false
		}
	}
	return true
}

// descendsThroughHolders walks parents that all hold the socket.
func descendsThroughHolders(snapshot domain.Snapshot, pid, root int, holders []int) bool {
	for range len(holders) {
		if pid == root {
			return true
		}
		if !slices.Contains(holders, pid) {
			return false
		}
		pid = snapshot.Processes[pid].PPID
	}
	return pid == root
}

// processRefusal applies every rule about one process.
func processRefusal(snapshot domain.Snapshot, process, root domain.Process) domain.Refusal {
	ancestors, isComplete := snapshot.Ancestors(process.PID)
	switch {
	case isProtected(snapshot, process):
		return domain.Refusal{Code: domain.CodeProtected}
	case isForwarder(process, ancestors):
		return domain.Refusal{Code: domain.CodeForwarder}
	case !isComplete:
		return domain.Refusal{Code: domain.CodeUnresolved}
	}
	if refusal := supervisionRefusal(snapshot, process, root, ancestors); refusal.Code != "" {
		return refusal
	}
	if !canSignal(snapshot.Host, process) {
		return permissionRefusal(process)
	}
	if process.Tracer != domain.No {
		return domain.Refusal{Code: domain.CodeDebugged}
	}
	if process.Suspended && (process.Ignored != domain.No || process.Blocked != domain.No) {
		return domain.Refusal{Code: domain.CodeStopBlocked}
	}
	return domain.Refusal{}
}

// canSignal requires the caller to own the process completely.
func canSignal(host domain.Host, process domain.Process) bool {
	return host.Privileged || process.UserID == host.CallerUID && process.RealUID == host.CallerUID
}

// permissionRefusal names the owner. An unknown owner is unresolved instead.
func permissionRefusal(process domain.Process) domain.Refusal {
	if process.UserID == "" {
		return domain.Refusal{Code: domain.CodeUnresolved}
	}
	return domain.Refusal{Code: domain.CodePermissionDenied, Advice: domain.Advice{NeedsAdmin: true, Owner: process.UserID}}
}

// isProtected is structural: PID 1, kernel, managers, Portly and its ancestors.
func isProtected(snapshot domain.Snapshot, process domain.Process) bool {
	host := snapshot.Host
	if process.PID <= 1 || process.Kernel || process.UserManager || process.PID == host.SelfPID {
		return true
	}
	own, isComplete := snapshot.Ancestors(host.SelfPID)
	isAncestor := slices.ContainsFunc(own, func(ancestor domain.Process) bool { return ancestor.PID == process.PID })
	if isAncestor || !isComplete {
		return true
	}
	isUnmanaged := host.ServiceManager != domain.Yes || process.Unit.Kind <= domain.UnitNone
	return isUnmanaged && process.TTY != domain.Yes && isSystemAccount(host, process.UserID)
}

// supervisionRefusal finds whatever would restart the process.
func supervisionRefusal(snapshot domain.Snapshot, process, root domain.Process, ancestors []domain.Process) domain.Refusal {
	if process.Service != domain.No {
		return domain.Refusal{Code: domain.CodeSupervised}
	}
	if !process.Job.Known {
		return domain.Refusal{Code: domain.CodeEvidenceUnavailable}
	}
	isJob := process.Job.Label != "" && (!process.Job.Application || process.Job.MainPID == process.PID)
	if isJob || hasSupervisor(process, ancestors) {
		return domain.Refusal{Code: domain.CodeSupervised}
	}
	if process.Unit.Kind == domain.UnitUnknown && snapshot.Host.ServiceManager == domain.Yes {
		return domain.Refusal{Code: domain.CodeEvidenceUnavailable}
	}
	if isUnitSupervised(process) {
		return domain.Refusal{Code: domain.CodeSupervised, Advice: unitAdvice(process, root)}
	}
	return domain.Refusal{}
}

// hasSupervisor checks ancestors up to the session leader on a terminal.
func hasSupervisor(process domain.Process, ancestors []domain.Process) bool {
	if process.PID == process.SessionID && process.TTY == domain.Yes {
		return false
	}
	for _, ancestor := range ancestors {
		if ancestor.PID <= 1 || ancestor.UserManager {
			return false
		}
		if isSupervisor(ancestor) {
			return true
		}
		if ancestor.PID == ancestor.SessionID && ancestor.TTY == domain.Yes {
			return false
		}
	}
	return false
}

// isUnitSupervised exempts terminal programs that merely share a unit.
func isUnitSupervised(process domain.Process) bool {
	if process.Unit.Kind != domain.UnitService {
		return false
	}
	isOnTerminal := process.TTY == domain.Yes && process.Unit.MainKnown && !isUnitMain(process)
	return !isOnTerminal
}

// isUnitMain matches a unit's main process and its direct children.
func isUnitMain(process domain.Process) bool {
	unit := process.Unit
	return unit.MainKnown && (process.PID == unit.MainPID || process.PPID == unit.MainPID)
}

// Anything else could be a shell metacharacter or an option
var printableUnit = regexp.MustCompile(`^[A-Za-z0-9@._:][A-Za-z0-9@._:-]*$`)

var unstoppableUnits = []string{"ssh", "sshd", "cron", "crond", "user@", "session-", "gdm", "sddm", "lightdm", "display-manager", "gnome-", "plasma-", "org.gnome", "dbus"}

// unitAdvice names the unit only when that is safe to print.
func unitAdvice(process, root domain.Process) domain.Advice {
	unit := process.Unit
	if !printableUnit.MatchString(unit.Name) {
		return domain.Advice{}
	}
	isSensitive := slices.ContainsFunc(unstoppableUnits, func(name string) bool { return strings.HasPrefix(unit.Name, name) })
	canStop := !isSensitive && (isUnitMain(process) || isUnitMain(root))
	return domain.Advice{Unit: unit.Name, UserUnit: unit.User, CanStopUnit: canStop}
}

// isReloaded matches a plain reloader parent. Display only.
func isReloaded(snapshot domain.Snapshot, process domain.Process) bool {
	parent, known := snapshot.Processes[process.PPID]
	if !known || len(process.Args) < 2 || len(parent.Args) < 2 || process.Dir == "" {
		return false
	}
	return parent.Exe == process.Exe && parent.Dir == process.Dir && parent.UserID == process.UserID &&
		slices.Equal(parent.Args[1:], process.Args[1:]) && len(snapshot.HeldBy(parent.PID)) == 0
}
