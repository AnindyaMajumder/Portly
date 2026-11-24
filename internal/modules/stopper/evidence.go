package stopper

import (
	"slices"
	"strconv"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// ownershipProblem finds evidence that is missing and could change the target.
func ownershipProblem(snapshot domain.Snapshot, sockets []domain.Socket) domain.Code {
	for _, socket := range sockets {
		if !socket.ID.Same(socket.ID) {
			return domain.CodeEvidenceUnavailable
		}
		if len(socket.Holders) == 0 || socket.Completeness() == domain.Unavailable {
			return unresolvedCode(snapshot.Host, socket)
		}
		if !hasVisibleCreator(snapshot, socket) {
			return domain.CodeUnresolved
		}
	}
	return ""
}

// unresolvedCode tells a permission problem from an unknown owner.
func unresolvedCode(host domain.Host, socket domain.Socket) domain.Code {
	isOthers := socket.CreatorUID != "" && socket.CreatorUID != host.CallerUID
	if isOthers && !host.Privileged {
		return domain.CodePermissionDenied
	}
	return domain.CodeUnresolved
}

// ownershipRefusal adds whose socket it is, when known.
func ownershipRefusal(snapshot domain.Snapshot, sockets []domain.Socket, code domain.Code) domain.Refusal {
	refusal := domain.Refusal{Code: code}
	if code != domain.CodePermissionDenied {
		return refusal
	}
	refusal.Advice.NeedsAdmin = true
	for _, socket := range sockets {
		if unresolvedCode(snapshot.Host, socket) == code {
			refusal.Advice.Owner = socket.CreatorUID
		}
	}
	return refusal
}

// hasVisibleCreator requires a known holder with the socket's user.
func hasVisibleCreator(snapshot domain.Snapshot, socket domain.Socket) bool {
	if socket.CreatorUID == "" {
		return false
	}
	return slices.ContainsFunc(socket.Holders, func(holder domain.Holder) bool {
		process, known := snapshot.Processes[holder.PID]
		return known && (process.UserID == socket.CreatorUID || process.RealUID == socket.CreatorUID)
	})
}

// engineProblem blocks when an engine that could own the port stayed silent.
func engineProblem(snapshot domain.Snapshot, process domain.Process) domain.Code {
	if snapshot.EnginesSeen != domain.Complete {
		return domain.CodeEngineUnresponsive
	}
	for _, engine := range snapshot.Engines {
		isSilent := engine.Answer == domain.AnswerTimeout || engine.Answer == domain.AnswerMalformed ||
			engine.Answer == domain.AnswerUnobserved || engine.Answer == domain.AnswerDenied
		if isSilent && (engine.OwnerUID == "" || engine.OwnerUID == process.UserID) {
			return domain.CodeEngineUnresponsive
		}
	}
	return ""
}

// descendantProblem blocks when a child's sockets could not be read.
func descendantProblem(snapshot domain.Snapshot, pid int) domain.Code {
	if snapshot.Host.ProcessTable != domain.Complete {
		return domain.CodeEvidenceUnavailable
	}
	for _, descendant := range snapshot.Descendants(pid) {
		process := snapshot.Processes[descendant]
		if !process.Zombie && process.Sockets != domain.Complete {
			return domain.CodeDescendantUnreadable
		}
	}
	return ""
}

const nobodyUID = 65534

// isSystemAccount treats unknown users as system accounts.
func isSystemAccount(host domain.Host, userID string) bool {
	if strings.HasPrefix(userID, "S-1-") {
		return !strings.HasPrefix(userID, "S-1-5-21-") && !strings.HasPrefix(userID, "S-1-12-1-")
	}
	uid, err := strconv.ParseUint(userID, 10, 32)
	return err != nil || uint32(uid) <= host.SystemUIDMax || uid == nobodyUID
}
