package cli

import (
	"encoding/json"
	"fmt"

	"github.com/AnindyaMajumder/Portly/internal/app"
	"github.com/AnindyaMajumder/Portly/internal/domain"
	"github.com/AnindyaMajumder/Portly/internal/platform/textsafe"
)

const schemaVersion = "1"

// document is the one JSON value of a command. See schema/portly.v1.schema.json.
type document struct {
	SchemaVersion string         `json:"schema_version"`
	Command       string         `json:"command"`
	Error         *errorJSON     `json:"error"`
	Warnings      []string       `json:"warnings"`
	Ports         *[]portJSON    `json:"ports,omitempty"`
	HiddenUDP     *int           `json:"hidden_udp,omitempty"`
	PlanID        optionalString `json:"plan_id,omitzero"`
	Targets       *[]targetJSON  `json:"targets,omitempty"`
	Results       *[]resultJSON  `json:"results,omitempty"`
	Update        *updateJSON    `json:"update,omitempty"`
}

type updateJSON struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Status  string `json:"status"`
}

// optionalString is left out, null, or a string.
type optionalString struct {
	isSet bool
	value *string
}

// IsZero leaves the field out of other commands.
func (o optionalString) IsZero() bool { return !o.isSet }

// MarshalJSON writes null or the string.
func (o optionalString) MarshalJSON() ([]byte, error) { return json.Marshal(o.value) }

type errorJSON struct {
	Code    domain.Code `json:"code"`
	Message string      `json:"message"`
	Hint    *string     `json:"hint"`
}

type evidenceJSON struct {
	Source       string `json:"source"`
	Completeness string `json:"completeness"`
}

type listenerJSON struct {
	Protocol     domain.Protocol `json:"protocol"`
	Family       string          `json:"family"`
	Address      string          `json:"address"`
	Port         uint16          `json:"port"`
	Connected    bool            `json:"connected"`
	Completeness string          `json:"completeness"`
	Evidence     []evidenceJSON  `json:"evidence"`
}

type endpointJSON struct {
	Protocol domain.Protocol `json:"protocol"`
	Address  string          `json:"address"`
	Port     uint16          `json:"port"`
}

type processJSON struct {
	PID        int     `json:"pid"`
	Name       *string `json:"name"`
	User       *string `json:"user"`
	Command    *string `json:"command"`
	Executable *string `json:"executable"`
	Directory  *string `json:"directory"`
}

type containerJSON struct {
	ID             string  `json:"id"`
	Name           *string `json:"name"`
	Image          *string `json:"image"`
	Engine         string  `json:"engine"`
	ComposeProject *string `json:"compose_project"`
	ComposeService *string `json:"compose_service"`
	Directory      *string `json:"directory"`
	Local          string  `json:"local"`
}

type projectJSON struct {
	Name       string            `json:"name"`
	Root       *string           `json:"root"`
	Confidence domain.Confidence `json:"confidence"`
	Evidence   string            `json:"evidence"`
}

type ownerJSON struct {
	Type      string         `json:"type"`
	Access    domain.Access  `json:"access"`
	User      *string        `json:"user"`
	Process   *processJSON   `json:"process"`
	Container *containerJSON `json:"container"`
	Project   *projectJSON   `json:"project"`
	Workers   []int          `json:"workers"`
	Listeners []listenerJSON `json:"listeners"`
}

type portJSON struct {
	Port      uint16           `json:"port"`
	Protocol  *domain.Protocol `json:"protocol"`
	Status    domain.Status    `json:"status"`
	Ambiguous bool             `json:"ambiguous"`
	Owners    []ownerJSON      `json:"owners"`
}

type outcomeJSON struct {
	Status        string         `json:"status"`
	Method        *domain.Method `json:"method"`
	ProcessExited *bool          `json:"process_exited"`
	PortReleased  *bool          `json:"port_released"`
	Resumed       bool           `json:"resumed"`
	KilledPIDs    []int          `json:"killed_pids"`
}

type targetJSON struct {
	Kind          domain.TargetKind `json:"kind"`
	Process       *processJSON      `json:"process"`
	Container     *containerJSON    `json:"container"`
	Project       *projectJSON      `json:"project"`
	Ports         []endpointJSON    `json:"ports"`
	Affected      []endpointJSON    `json:"affected"`
	Children      []processJSON     `json:"children"`
	Suspended     bool              `json:"suspended"`
	Paused        bool              `json:"paused"`
	AutoRemove    bool              `json:"auto_remove"`
	Reloader      bool              `json:"reloader"`
	SharedNetwork []string          `json:"shared_network"`
	Graceful      bool              `json:"graceful"`
	Outcome       *outcomeJSON      `json:"outcome"`
}

type resultJSON struct {
	Port     uint16          `json:"port"`
	Protocol domain.Protocol `json:"protocol"`
	Status   string          `json:"status"`
	Target   *int            `json:"target"`
	Error    *errorJSON      `json:"error"`
}

// writeJSON prints the document, without HTML escapes or colour.
func (s *session) writeJSON(value document) error {
	encoder := json.NewEncoder(s.console.Out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// newDocument fills the fields every document has.
func newDocument(command string, warnings []string) document {
	return document{SchemaVersion: schemaVersion, Command: command, Warnings: cleanAll(warnings)}
}

// failureDocument is the document of a command that failed.
func failureDocument(command string, failure *domain.Error) document {
	result := newDocument(command, nil)
	result.Error = jsonError(failure)
	return result
}

// reportDocument is the document of list and inspect.
func reportDocument(command string, report app.Report) document {
	result := newDocument(command, append(append([]string{}, report.Warnings...), report.Notes...))
	ports := []portJSON{}
	for _, port := range report.Ports {
		ports = append(ports, jsonPort(port))
	}
	result.Ports = &ports
	if command == "list" {
		result.HiddenUDP = &report.HiddenUDP
	}
	return result
}

// stopDocument is the document of stop. No outcomes means nothing ran.
func (s *session) stopDocument(plan app.Plan, outcomes []domain.Outcome) document {
	document := newDocument("stop", append(append([]string{}, plan.Warnings...), plan.Notes...))
	document.PlanID = optionalString{isSet: true, value: optional(plan.Stop.ID)}
	targets, results := []targetJSON{}, []resultJSON{}
	for i, target := range plan.Targets {
		encoded := jsonTarget(target)
		if outcomes != nil {
			encoded.Outcome = jsonOutcome(outcomes[i])
		}
		targets = append(targets, encoded)
	}
	for _, result := range plan.Stop.Results {
		results = append(results, s.jsonResult(plan, result, outcomes))
	}
	document.Targets, document.Results = &targets, &results
	return document
}

// jsonResult is one named port.
func (s *session) jsonResult(plan app.Plan, result domain.PortResult, outcomes []domain.Outcome) resultJSON {
	encoded := resultJSON{Port: result.Endpoint.Port, Protocol: result.Endpoint.Protocol, Status: "refused"}
	if failure := s.failureOf(plan, result, outcomes); failure != nil {
		encoded.Error = jsonError(failure)
	}
	if result.Refusal.Code == domain.CodeNotListening {
		encoded.Status = "not_listening"
	}
	if result.Target >= 0 {
		encoded.Target, encoded.Status = &result.Target, "allowed"
		if outcomes != nil {
			encoded.Status = statusOf(outcomes[result.Target])
		}
	}
	return encoded
}

// statusOf names an outcome.
func statusOf(outcome domain.Outcome) string {
	_, isResult := results[outcome.Code]
	switch {
	case outcome.Code == "":
		return "stopped"
	case isResult || outcome.Code == domain.CodeCancelled:
		return string(outcome.Code)
	}
	return "refused"
}

// jsonOutcome keeps unknown facts null.
func jsonOutcome(outcome domain.Outcome) *outcomeJSON {
	encoded := &outcomeJSON{
		Status: statusOf(outcome), ProcessExited: known(outcome.ProcessExited), PortReleased: known(outcome.PortReleased),
		Resumed: outcome.Resumed, KilledPIDs: append([]int{}, outcome.Killed...),
	}
	if outcome.Method != "" {
		encoded.Method = &outcome.Method
	}
	return encoded
}

// known turns a tri-state into true, false or null.
func known(fact domain.Tri) *bool {
	if fact == domain.Unknown {
		return nil
	}
	value := fact == domain.Yes
	return &value
}

// jsonError keeps an empty hint null.
func jsonError(failure *domain.Error) *errorJSON {
	return &errorJSON{Code: failure.Code, Message: clean(failure.Message), Hint: optional(failure.Hint)}
}

// jsonPort is one port of a report.
func jsonPort(port domain.PortReport) portJSON {
	encoded := portJSON{Port: port.Ref.Port, Status: port.Status, Ambiguous: port.Ambiguous, Owners: []ownerJSON{}}
	if port.Ref.Protocol != "" {
		encoded.Protocol = &port.Ref.Protocol
	}
	for _, owner := range port.Owners {
		encoded.Owners = append(encoded.Owners, jsonOwner(owner))
	}
	return encoded
}

// jsonOwner is one owner of a port.
func jsonOwner(owner domain.Owner) ownerJSON {
	encoded := ownerJSON{
		Type: owner.Type, Access: owner.Access, User: optional(owner.User), Process: jsonProcess(owner.Process),
		Container: jsonContainer(owner.Container, owner.Proven), Project: jsonProject(owner.Project),
		Workers: append([]int{}, owner.Workers...), Listeners: []listenerJSON{},
	}
	for _, socket := range owner.Listeners {
		encoded.Listeners = append(encoded.Listeners, jsonListener(socket))
	}
	return encoded
}

// jsonListener is one socket with its evidence.
func jsonListener(socket domain.Socket) listenerJSON {
	encoded := listenerJSON{
		Protocol: socket.ID.Protocol, Family: fmt.Sprintf("ipv%d", socket.ID.Family), Port: socket.ID.Port,
		Address: clean(socket.ID.Addr.WithZone(socket.ID.Scope).String()), Connected: socket.Connected,
		Completeness: socket.Completeness().String(), Evidence: []evidenceJSON{},
	}
	for _, evidence := range socket.Evidence {
		encoded.Evidence = append(encoded.Evidence, evidenceJSON{Source: evidence.Source, Completeness: evidence.Completeness.String()})
	}
	return encoded
}

// jsonProcess cleans names and masks the command.
func jsonProcess(process *domain.Process) *processJSON {
	if process == nil {
		return nil
	}
	return &processJSON{
		PID: process.PID, Name: optional(process.Name), User: optional(process.User),
		Command: optional(textsafe.CommandLine(process.Args)), Executable: optional(process.Exe), Directory: optional(process.Dir),
	}
}

// jsonContainer says whether its engine is proven local.
func jsonContainer(container *domain.Container, isProven bool) *containerJSON {
	if container == nil {
		return nil
	}
	local := "unproven"
	if isProven {
		local = "proven"
	}
	return &containerJSON{
		ID: container.ID, Name: optional(container.Name), Image: optional(container.Image), Engine: clean(container.Engine),
		ComposeProject: optional(container.ComposeProject), ComposeService: optional(container.ComposeService),
		Directory: optional(container.Directory), Local: local,
	}
}

// jsonProject is null when there is no evidence.
func jsonProject(project *domain.Project) *projectJSON {
	if project == nil {
		return nil
	}
	return &projectJSON{Name: clean(project.Name), Root: optional(project.Root), Confidence: project.Confidence, Evidence: clean(project.Evidence)}
}

// jsonTarget is one stop target and everything it affects.
func jsonTarget(target app.Target) targetJSON {
	encoded := targetJSON{
		Kind: target.Stop.Kind, Process: jsonProcess(target.Process), Container: jsonContainer(target.Container, target.Proven),
		Project: jsonProject(target.Project), Ports: []endpointJSON{}, Affected: []endpointJSON{}, Children: []processJSON{},
		Suspended: target.Stop.Suspended, Paused: target.Stop.Paused, AutoRemove: target.Stop.AutoRemove,
		Reloader: target.Stop.Reloader, SharedNetwork: cleanAll(target.Stop.SharesNet), Graceful: target.Stop.GracefulAvailable,
	}
	for _, socket := range target.Stop.Named {
		encoded.Ports = append(encoded.Ports, jsonEndpoint(socket.AddressPort))
	}
	for _, binding := range target.Stop.Bindings {
		encoded.Ports = append(encoded.Ports, endpointJSON{Protocol: binding.Protocol, Address: clean(binding.Address().String()), Port: binding.HostPort})
	}
	for _, other := range target.Stop.Other {
		encoded.Affected = append(encoded.Affected, jsonEndpoint(other))
	}
	for _, child := range target.Children {
		encoded.Children = append(encoded.Children, *jsonProcess(&child))
	}
	return encoded
}

// jsonEndpoint is one bound address.
func jsonEndpoint(address domain.AddressPort) endpointJSON {
	return endpointJSON{Protocol: address.Protocol, Address: clean(address.Addr.WithZone(address.Scope).String()), Port: address.Port}
}

// optional cleans text and turns an empty one into null.
func optional(text string) *string {
	if text == "" {
		return nil
	}
	text = clean(text)
	return &text
}

// cleanAll cleans a list and never returns null.
func cleanAll(texts []string) []string {
	cleaned := []string{}
	for _, text := range texts {
		cleaned = append(cleaned, clean(text))
	}
	return cleaned
}
