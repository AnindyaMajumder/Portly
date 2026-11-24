package domain

import (
	"cmp"
	"errors"
	"slices"
	"sync/atomic"
)

// TargetKind zero value is not stoppable.
type TargetKind string

const (
	KindProcess   TargetKind = "process"
	KindFamily    TargetKind = "family"
	KindContainer TargetKind = "container"
)

// Method zero value is not executable.
type Method string

const (
	Graceful Method = "graceful"
	Forced   Method = "forced"
)

// StopRequest is what the user named.
type StopRequest struct {
	Endpoints []Endpoint
	Force     bool
}

// StopTarget is one process, family or container to stop.
type StopTarget struct {
	Kind      TargetKind
	Root      ProcessID
	Container ContainerRef
	// Family members holding the socket, root included
	Members  []ProcessID
	Named    []SocketID
	Bindings []Binding
	// Other non-ephemeral endpoints this stop closes
	Other     []AddressPort
	Children  []ProcessID
	SharesNet []string

	Suspended         bool
	Paused            bool
	AutoRemove        bool
	Reloader          bool
	GracefulAvailable bool
}

// Advice is structured so hostile names never become commands.
type Advice struct {
	Unit        string
	UserUnit    bool
	CanStopUnit bool
	NeedsAdmin  bool
	UDPOnly     bool
	Owner       string
}

// Refusal says why an endpoint is not stopped.
type Refusal struct {
	Code   Code
	Advice Advice
}

// PortResult points an endpoint at its target or refusal.
type PortResult struct {
	Endpoint Endpoint
	// Index into StopPlan.Targets, -1 when refused
	Target  int
	Refusal Refusal
}

// StopPlan is every target and refusal of one command.
type StopPlan struct {
	ID      string
	Targets []StopTarget
	Results []PortResult
}

// Delivery is what happened to a stop request.
type Delivery struct {
	// The OS or engine accepted the request
	Accepted bool
	Resumed  bool
	// Set when nothing was delivered
	Code   Code
	Killed []int
}

// ContainerState is a container's state during verification.
type ContainerState struct {
	Known     bool
	Running   bool
	StartedAt string
}

var (
	errNotApproved = errors.New("stop was not approved")
	errChanged     = NewError(CodeChanged, "The target changed since it was shown", "Run the command again and check the new target")
)

// Approval lets an executor act once, with one method. Zero refuses.
type Approval struct {
	target StopTarget
	method Method
	used   atomic.Bool
}

// Approve compares a fresh target with the confirmed one.
func Approve(planned, current StopTarget, method Method) (*Approval, error) {
	if method != Graceful && method != Forced {
		return nil, errNotApproved
	}
	if !sameTarget(planned, current) {
		return nil, errChanged
	}
	if method == Graceful && !sameReach(planned, current) {
		return nil, errChanged
	}
	return &Approval{target: current, method: method}, nil
}

// Take returns the target once, only for the approved method.
func (a *Approval) Take(method Method) (StopTarget, error) {
	if a == nil || a.method == "" || a.method != method || !a.used.CompareAndSwap(false, true) {
		return StopTarget{}, errNotApproved
	}
	return a.target, nil
}

// sameTarget compares identity and requires a still-held named socket.
func sameTarget(planned, current StopTarget) bool {
	if planned.Kind == "" || planned.Kind != current.Kind {
		return false
	}
	if planned.Kind == KindContainer {
		return planned.Container.Same(current.Container)
	}
	return planned.Root.Same(current.Root) && sharesSocket(planned.Named, current.Named)
}

func sharesSocket(planned, current []SocketID) bool {
	return slices.ContainsFunc(planned, func(id SocketID) bool { return containsSocket(current, id) })
}

// sameReach refuses a target that gained anything since the prompt.
func sameReach(planned, current StopTarget) bool {
	gainedState := current.Suspended && !planned.Suspended || current.Paused && !planned.Paused
	if gainedState || len(current.Named) != len(planned.Named) || len(current.Bindings) != len(planned.Bindings) {
		return false
	}
	for _, socket := range current.Named {
		if !containsSocket(planned.Named, socket) {
			return false
		}
	}
	for _, binding := range current.Bindings {
		if !slices.Contains(planned.Bindings, binding) {
			return false
		}
	}
	for _, endpoint := range current.Other {
		if !slices.Contains(planned.Other, endpoint) {
			return false
		}
	}
	return current.GracefulAvailable == planned.GracefulAvailable
}

func containsSocket(sockets []SocketID, wanted SocketID) bool {
	return slices.ContainsFunc(sockets, wanted.Same)
}

// SortAddresses orders endpoints for stable output.
func SortAddresses(addresses []AddressPort) {
	slices.SortFunc(addresses, func(a, b AddressPort) int {
		return cmp.Or(a.Endpoint.Compare(b.Endpoint), a.Addr.Compare(b.Addr), cmp.Compare(a.Scope, b.Scope))
	})
}
