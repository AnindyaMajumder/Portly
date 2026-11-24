package domain

import "errors"

// Code is a stable, machine-readable reason.
type Code string

const (
	CodeInternal     Code = "internal"
	CodeUsage        Code = "usage"
	CodeNotListening Code = "not_listening"

	CodePermissionDenied     Code = "permission_denied"
	CodeUnresolved           Code = "unresolved_owner"
	CodeEvidenceUnavailable  Code = "evidence_unavailable"
	CodeEvidenceUnstable     Code = "evidence_unstable"
	CodeDescendantUnreadable Code = "descendant_unreadable"
	CodeEngineUnresponsive   Code = "engine_unresponsive"

	CodeAmbiguous         Code = "ambiguous_owner"
	CodeProtected         Code = "protected_process"
	CodeForwarder         Code = "port_forwarder"
	CodeSupervised        Code = "supervised_process"
	CodeContainerManaged  Code = "container_managed"
	CodeContainerUnproven Code = "container_unproven"
	CodeContainerSelf     Code = "container_self"
	CodeRefusedElsewhere  Code = "refused_with_other_port"
	CodeChanged           Code = "target_changed"
	CodeNoGraceful        Code = "no_graceful_method"
	CodeDebugged          Code = "being_debugged"
	CodeStopBlocked       Code = "stop_signal_blocked"
	CodeEngineRejected    Code = "engine_rejected_stop"
	CodePlatform          Code = "platform_unverified"

	CodeConfirmationRequired Code = "confirmation_required"
	CodeCancelled            Code = "cancelled"

	CodeDraining       Code = "draining"
	CodeStillListening Code = "still_listening"
	CodeRestarted      Code = "restarted"
	CodeTakenOver      Code = "taken_over"
	CodePending        Code = "pending"
	CodeUnknownOutcome Code = "unknown"
)

// ExitMixed covers several ports with different exit codes.
const ExitMixed = 8

var exitCodes = map[Code]int{
	CodeInternal: 1, CodeUsage: 2, CodeNotListening: 3,

	CodePermissionDenied: 4, CodeUnresolved: 4, CodeEvidenceUnavailable: 4,
	CodeEvidenceUnstable: 4, CodeDescendantUnreadable: 4, CodeEngineUnresponsive: 4,

	CodeAmbiguous: 5, CodeProtected: 5, CodeForwarder: 5, CodeSupervised: 5,
	CodeContainerManaged: 5, CodeContainerUnproven: 5, CodeContainerSelf: 5,
	CodeRefusedElsewhere: 5, CodeChanged: 5, CodeNoGraceful: 5, CodeDebugged: 5,
	CodeStopBlocked: 5, CodeEngineRejected: 5, CodePlatform: 5,

	CodeConfirmationRequired: 6, CodeCancelled: 6,

	CodeDraining: 7, CodeStillListening: 7, CodeRestarted: 7, CodeTakenOver: 7,
	CodePending: 7, CodeUnknownOutcome: 7,
}

// Codes lists every reason code, for contract tests.
func Codes() []Code {
	codes := make([]Code, 0, len(exitCodes))
	for code := range exitCodes {
		codes = append(codes, code)
	}
	return codes
}

// ExitCode maps the reason to the documented exit status.
func (c Code) ExitCode() int {
	if exit, ok := exitCodes[c]; ok {
		return exit
	}
	return 1
}

// Error is a user-facing failure with a next step.
type Error struct {
	Code    Code
	Message string
	Hint    string
}

// Error is the message shown to the user.
func (e *Error) Error() string { return e.Message }

// NewError builds a user-facing error with a next step.
func NewError(code Code, message, hint string) *Error {
	return &Error{Code: code, Message: message, Hint: hint}
}

// AsError turns any error into a user-facing one.
func AsError(err error) *Error {
	var known *Error
	if errors.As(err, &known) {
		return known
	}
	return NewError(CodeInternal, err.Error(), "Run again with --verbose for details")
}
