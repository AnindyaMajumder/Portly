package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/app"
	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// reasons explain refusals. Each fits after "Can't stop port 8000: ".
var reasons = map[domain.Code]struct{ message, hint string }{
	domain.CodeUnresolved:           {"its owner could not be determined", "Run 'portly %s' to see what is known"},
	domain.CodeEvidenceUnavailable:  {"the system did not give enough information", "Try again, or run portly as administrator"},
	domain.CodeEvidenceUnstable:     {"its owner keeps changing", "Something restarts it. Find and stop that first"},
	domain.CodeDescendantUnreadable: {"one of its child processes can't be checked", "Run portly as administrator, or stop the child first"},
	domain.CodeEngineUnresponsive:   {"a container engine that may own it did not answer", "Check that Docker or Podman works, then try again"},
	domain.CodeAmbiguous:            {"several owners share it", "Run 'portly %s' to see them, then stop the right one yourself"},
	domain.CodeProtected:            {"it belongs to the system or to this session", "Use the system's own tools if you really need to"},
	domain.CodeForwarder:            {"its process only forwards to something else", "Stop the container, virtual machine or remote service behind it"},
	domain.CodeSupervised:           {"a supervisor would restart it", "Stop it through its supervisor"},
	domain.CodeContainerManaged:     {"an orchestrator manages its container", "Stop it through Swarm, Kubernetes or systemd"},
	domain.CodeContainerUnproven:    {"its container is not proven to run on this machine", "Stop it with the engine's own command"},
	domain.CodeContainerSelf:        {"portly may be running in that container", "Run portly outside the container"},
	domain.CodeRefusedElsewhere:     {"the same owner was refused on another port you named", "See that port's reason first"},
	domain.CodeChanged:              {"it changed since it was checked", "Run 'portly %s' and check again"},
	domain.CodeNoGraceful:           {"it has no graceful way to stop", "To kill it: portly stop %s --force"},
	domain.CodeDebugged:             {"it is being debugged", "Detach the debugger first"},
	domain.CodeStopBlocked:          {"it is suspended and would ignore the stop", "Resume it with fg or bg, then try again"},
	domain.CodeEngineRejected:       {"its engine can't stop it without a kill timeout", "Stop it with the engine's own command"},
	domain.CodePlatform:             {"stopping is not verified on this platform yet", "Listing works. Use the system's own tools to stop it"},
	domain.CodeCancelled:            {"it was cancelled", "Run the command again when ready"},
	domain.CodeInternal:             {"of an internal error", "Run again with --verbose and report it"},
	domain.CodePermissionDenied:     {"it belongs to another user", "Run portly as that user"},
}

// explain turns a refusal into a message and the next step.
func (s *session) explain(endpoint domain.Endpoint, refusal domain.Refusal) *domain.Error {
	port := portText(endpoint)
	if refusal.Code == domain.CodeNotListening {
		hint := "Run 'portly' to see what is listening"
		if refusal.Advice.UDPOnly {
			hint = fmt.Sprintf("Only UDP uses it. To stop that: portly stop %d/udp", endpoint.Port)
		}
		return domain.NewError(refusal.Code, "Nothing is listening on port "+port, hint)
	}
	reason, known := reasons[refusal.Code]
	if !known {
		reason = reasons[domain.CodeInternal]
	}
	message, hint := reason.message, strings.ReplaceAll(reason.hint, "%s", port)
	switch {
	case refusal.Code == domain.CodePermissionDenied && refusal.Advice.NeedsAdmin:
		message, hint = s.permission(refusal.Advice, port)
	case refusal.Code == domain.CodeSupervised && refusal.Advice.Unit != "":
		hint = unitHint(refusal.Advice)
	}
	return domain.NewError(refusal.Code, "Can't stop port "+port+": "+message, hint)
}

// permission names the owner and prints the command, never runs it.
func (s *session) permission(advice domain.Advice, port string) (string, string) {
	message := "it belongs to another user"
	if owner := app.UserName(advice.Owner); owner != "" {
		message += " (" + owner + ")"
	}
	if s.console.Windows {
		return message, "Run 'portly stop " + port + "' in an administrator terminal"
	}
	hint := "To try as administrator:  sudo " + quote(s.console.Executable) + " stop " + port
	return message, hint + "  (system services like sshd are still refused)"
}

// unitHint prints a stop command only when the policy called it safe.
func unitHint(advice domain.Advice) string {
	scope, prefix := "", "sudo "
	if advice.UserUnit {
		scope, prefix = "--user ", ""
	}
	if advice.CanStopUnit {
		return "To stop the service:  " + prefix + "systemctl " + scope + "stop " + advice.Unit
	}
	return "To see the service:  systemctl " + scope + "status " + advice.Unit
}

var plainPath = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// quote makes a path safe to paste into a shell.
func quote(path string) string {
	path = clean(path)
	if path == "" {
		return "portly"
	}
	if plainPath.MatchString(path) && !strings.HasPrefix(path, "-") {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// portText omits the protocol that a bare port already means.
func portText(endpoint domain.Endpoint) string {
	if endpoint.Protocol == domain.UDP {
		return endpoint.String()
	}
	return fmt.Sprint(endpoint.Port)
}

// results explain what happened after something was delivered.
var results = map[domain.Code]struct{ message, hint string }{
	domain.CodeDraining:       {"released the port but is still running", "It may be finishing work. Check again in a moment"},
	domain.CodeStillListening: {"is still listening", "To kill it: portly stop %s --force"},
	domain.CodeRestarted:      {"was started again by something", "Stop whatever restarts it first"},
	domain.CodeTakenOver:      {"stopped, but another program uses the port now", "Run 'portly %s' to see it"},
	domain.CodePending:        {"was asked to stop and is still running, the request stays pending at the engine", "To kill it: portly stop %s --force"},
	domain.CodeUnknownOutcome: {"was asked to stop, but the result could not be observed", "Run 'portly %s' to check"},
}

// explainOutcome describes an unsuccessful stop.
func (s *session) explainOutcome(endpoint domain.Endpoint, name string, outcome domain.Outcome) *domain.Error {
	result, known := results[outcome.Code]
	if !known {
		failure := s.explain(endpoint, domain.Refusal{Code: outcome.Code, Advice: outcome.Advice})
		if outcome.Delivered {
			failure.Message = name + " was asked to stop. " + failure.Message
		}
		return failure
	}
	hint := strings.ReplaceAll(result.hint, "%s", portText(endpoint))
	return domain.NewError(outcome.Code, name+" "+result.message, hint)
}
