package stopper

import (
	"cmp"
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
			refusal.Advice.Owner = cmp.Or(socket.CreatorUser, socket.CreatorUID)
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
	if hasSilentEngine(snapshot, process.UserID) {
		return domain.CodeEngineUnresponsive
	}
	return ""
}

// hasSilentEngine finds an engine of this user that could own the port.
func hasSilentEngine(snapshot domain.Snapshot, userID string) bool {
	return hasEngine(snapshot, userID, slices.Concat(unanswered, []domain.Answer{domain.AnswerDenied}))
}

// hasUnansweredEngine leaves out engines this caller may not ask at all.
func hasUnansweredEngine(snapshot domain.Snapshot, userID string) bool {
	return hasEngine(snapshot, userID, unanswered)
}

// Answers of an engine that could be asked and said nothing usable
var unanswered = []domain.Answer{domain.AnswerTimeout, domain.AnswerMalformed, domain.AnswerUnobserved}

// hasEngine finds an engine of this user, or of an unknown one, by its answer.
func hasEngine(snapshot domain.Snapshot, userID string, answers []domain.Answer) bool {
	return slices.ContainsFunc(snapshot.Engines, func(engine domain.Engine) bool {
		isTheirs := engine.OwnerUID == "" || engine.OwnerUID == userID
		return isTheirs && slices.Contains(answers, engine.Answer)
	})
}

// descendantProblem blocks when a child's sockets could not be read.
func descendantProblem(snapshot domain.Snapshot, pid int) domain.Code {
	for _, descendant := range snapshot.Descendants(pid) {
		process := snapshot.Processes[descendant]
		if !process.Zombie && process.Sockets != domain.Complete {
			return domain.CodeDescendantUnreadable
		}
	}
	return ""
}

// nobody on Linux and on macOS
var nobodyUIDs = []uint64{65534, 4294967294}

// isSystemAccount treats unknown users as system accounts.
func isSystemAccount(host domain.Host, userID string) bool {
	if strings.HasPrefix(userID, "S-1-") {
		return !strings.HasPrefix(userID, "S-1-5-21-") && !strings.HasPrefix(userID, "S-1-12-1-")
	}
	uid, err := strconv.ParseUint(userID, 10, 32)
	return err != nil || uint32(uid) <= host.SystemUIDMax || slices.Contains(nobodyUIDs, uid)
}
