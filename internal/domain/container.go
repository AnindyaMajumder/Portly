package domain

import "net/netip"

// Binding maps a host address to a container port.
type Binding struct {
	HostAddr      netip.Addr
	HostPort      uint16
	ContainerPort uint16
	Protocol      Protocol
}

// Endpoint is the host port and protocol of a binding.
func (b Binding) Endpoint() Endpoint { return Endpoint{Port: b.HostPort, Protocol: b.Protocol} }

// Address is the host address, where unset means every IPv4 address.
func (b Binding) Address() netip.Addr {
	if !b.HostAddr.IsValid() {
		return netip.IPv4Unspecified()
	}
	return b.HostAddr
}

// Matches compares a socket on the same endpoint by exact address.
func (b Binding) Matches(id SocketID) bool {
	address := id.Addr.WithZone("")
	return b.HostAddr == address || !b.HostAddr.IsValid() && address.IsUnspecified()
}

// Container is a running container and its published ports.
type Container struct {
	ID         string
	Engine     string
	StartedAt  string
	Name       string
	Image      string
	StopSignal string
	InitPID    int
	Inspected  bool
	Running    bool
	Paused     bool
	AutoRemove bool
	HostNet    bool
	// Swarm, Kubernetes or systemd, when one manages it
	Orchestrator   string
	ComposeProject string
	ComposeService string
	Directory      string
	Ports          []Binding
	SharesNet      []string
}

// Publishes returns the bindings for one endpoint.
func (c Container) Publishes(endpoint Endpoint) []Binding {
	var found []Binding
	for _, binding := range c.Ports {
		if binding.Endpoint() == endpoint {
			found = append(found, binding)
		}
	}
	return found
}

// ContainerRef is the identity a container stop is bound to.
type ContainerRef struct{ Engine, ID, StartedAt string }

// Same is false when anything is unknown.
func (c ContainerRef) Same(other ContainerRef) bool {
	return c.Engine != "" && c.ID != "" && c.StartedAt != "" && c == other
}

// Ref is the identity a stop of this container is bound to.
func (c Container) Ref() ContainerRef {
	return ContainerRef{Engine: c.Engine, ID: c.ID, StartedAt: c.StartedAt}
}

// Answer is how an engine socket responded.
type Answer uint8

const (
	AnswerUnobserved Answer = iota
	AnswerAbsent
	AnswerRefused
	AnswerDenied
	AnswerTimeout
	AnswerMalformed
	AnswerOK
)

// Engine is one local Docker or Podman socket.
type Engine struct {
	ID         string
	Kind       string
	OwnerUID   string
	Answer     Answer
	Containers []Container
}
