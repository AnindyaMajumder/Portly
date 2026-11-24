// Package domain holds Portly's shared types and pure rules.
package domain

import (
	"cmp"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

type Protocol string

const (
	TCP Protocol = "tcp"
	UDP Protocol = "udp"
)

// PortRef is a port the user named. Empty protocol means any.
type PortRef struct {
	Port     uint16
	Protocol Protocol
}

func (r PortRef) String() string {
	if r.Protocol == "" {
		return strconv.Itoa(int(r.Port))
	}
	return fmt.Sprintf("%d/%s", r.Port, r.Protocol)
}

// Matches reports whether a port and protocol belong to this reference.
func (r PortRef) Matches(port uint16, protocol Protocol) bool {
	return r.Port == port && (r.Protocol == "" || r.Protocol == protocol)
}

// StopEndpoint applies the rule that a bare port means TCP.
func (r PortRef) StopEndpoint() Endpoint {
	if r.Protocol == UDP {
		return Endpoint{Port: r.Port, Protocol: UDP}
	}
	return Endpoint{Port: r.Port, Protocol: TCP}
}

// Endpoint is a port with a mandatory protocol.
type Endpoint struct {
	Port     uint16
	Protocol Protocol
}

func (e Endpoint) String() string { return fmt.Sprintf("%d/%s", e.Port, e.Protocol) }

// Compare orders by port, then protocol.
func (e Endpoint) Compare(other Endpoint) int {
	return cmp.Or(cmp.Compare(e.Port, other.Port), cmp.Compare(e.Protocol, other.Protocol))
}

// AddressPort is one bound address. Scope is an IPv6 zone.
type AddressPort struct {
	Endpoint
	Addr  netip.Addr
	Scope string
}

func (a AddressPort) String() string {
	return netip.AddrPortFrom(a.Addr.WithZone(a.Scope), a.Port).String()
}

// Range is an inclusive port range. Zero contains nothing.
type Range struct{ First, Last uint16 }

func (r Range) Contains(port uint16) bool {
	return r.First != 0 && port >= r.First && port <= r.Last
}

// ParsePortRef accepts 8000, :8000, 8000/tcp and 8000/udp.
func ParsePortRef(text string) (PortRef, error) {
	number, protocol, hasProtocol := strings.Cut(strings.TrimPrefix(text, ":"), "/")
	port, err := strconv.ParseUint(number, 10, 16)
	if err != nil || port == 0 || strings.HasPrefix(number, "+") {
		return PortRef{}, NewError(CodeUsage, fmt.Sprintf("%q is not a port", text), "Use a number from 1 to 65535, e.g. portly 8000")
	}
	ref := PortRef{Port: uint16(port)}
	if !hasProtocol {
		return ref, nil
	}
	ref.Protocol = Protocol(strings.ToLower(protocol))
	if ref.Protocol != TCP && ref.Protocol != UDP {
		return PortRef{}, NewError(CodeUsage, fmt.Sprintf("%q is not a protocol", protocol), fmt.Sprintf("Use %d/tcp or %d/udp", port, port))
	}
	return ref, nil
}
