package domain

import (
	"net/netip"
	"slices"
	"testing"
)

var loopback = netip.MustParseAddr("127.0.0.1")

// engineWorld has one proven container publishing port 8000.
func engineWorld() Snapshot {
	container := Container{
		ID: "db", InitPID: 30, StartedAt: "t", Running: true,
		Ports: []Binding{{HostAddr: loopback, HostPort: 8000, ContainerPort: 80, Protocol: TCP}},
	}
	return Snapshot{
		Engines: []Engine{{ID: "a", Containers: []Container{container}}, {ID: "b", Containers: []Container{container}}},
		Processes: map[int]Process{
			30: {ContainerIDs: []string{"db"}, ContainerPath: "/docker-db"},
			31: {ContainerIDs: []string{"db"}, ContainerPath: "/docker-db"},
			32: {ContainerIDs: []string{"db"}, ContainerPath: "/user/docker-db"},
			33: {},
		},
	}
}

// TestPublishersListsEachContainerOnce covers two sockets to one engine.
func TestPublishersListsEachContainerOnce(t *testing.T) {
	snapshot := engineWorld()
	found := snapshot.Publishers(Endpoint{8000, TCP})
	if len(found) != 1 || found[0].Container.Ref() != (ContainerRef{"a", "db", "t"}) {
		t.Errorf("got %+v", found)
	}
	for _, other := range []Endpoint{{8000, UDP}, {8001, TCP}} {
		if got := snapshot.Publishers(other); len(got) != 0 {
			t.Errorf("%v: %+v", other, got)
		}
	}
	snapshot.Engines[0].Containers[0].Running = false
	snapshot.Engines = snapshot.Engines[:1]
	if got := snapshot.Publishers(Endpoint{8000, TCP}); len(got) != 0 {
		t.Errorf("a stopped container publishes nothing: %+v", got)
	}
}

// TestContainerMembership needs the same cgroup path as the init process.
func TestContainerMembership(t *testing.T) {
	snapshot := engineWorld()
	container := snapshot.Engines[0].Containers[0]
	for pid, want := range map[int]bool{30: true, 31: true, 32: false, 33: false, 99: false} {
		if got := snapshot.IsInside(pid, container); got != want {
			t.Errorf("%d: %v", pid, got)
		}
	}
	for name, change := range map[string]func(*Container){
		"no id": func(c *Container) { c.ID = "" }, "no init": func(c *Container) { c.InitPID = 0 }, "init is 1": func(c *Container) { c.InitPID = 1 },
		"other init": func(c *Container) { c.InitPID = 33 }, "missing init": func(c *Container) { c.InitPID = 99 },
	} {
		changed := container
		change(&changed)
		if snapshot.ProvenLocal(changed) || snapshot.IsInside(31, changed) {
			t.Errorf("%s is not proven", name)
		}
	}
	if !snapshot.ProvenLocal(container) {
		t.Error("the container is proven")
	}
}

// TestBindingAddresses covers exact, unset and scoped addresses.
func TestBindingAddresses(t *testing.T) {
	id := func(address string) SocketID {
		return SocketID{AddressPort: AddressPort{Addr: netip.MustParseAddr(address)}}
	}
	exact, unset := Binding{HostAddr: loopback}, Binding{}
	if !exact.Matches(id("127.0.0.1")) || exact.Matches(id("0.0.0.0")) {
		t.Error("exact")
	}
	if !unset.Matches(id("0.0.0.0")) || !unset.Matches(id("::")) || unset.Matches(id("127.0.0.1")) {
		t.Error("unset")
	}
	if !(Binding{HostAddr: netip.MustParseAddr("fe80::1")}).Matches(id("fe80::1%eth0")) {
		t.Error("scoped")
	}
	if exact.Address() != loopback || unset.Address() != netip.IPv4Unspecified() {
		t.Error("address")
	}
}

// TestHolders covers sockets by port and by process.
func TestHolders(t *testing.T) {
	first := Socket{ID: testSocket("1"), Holders: []Holder{{PID: 12}, {PID: 10}, {PID: 10}}}
	second := Socket{ID: testSocket("2"), Holders: []Holder{{PID: 11}}}
	second.ID.Port = 9000
	snapshot := Snapshot{Sockets: []Socket{first, second}}
	if got := HolderPIDs(snapshot.Sockets); !slices.Equal(got, []int{10, 11, 12}) {
		t.Errorf("holders %v", got)
	}
	if got := snapshot.On(Endpoint{9000, TCP}); len(got) != 1 || got[0].ID.OSID != "2" {
		t.Errorf("on %+v", got)
	}
	if got := snapshot.HeldBy(10); len(got) != 1 || got[0].ID.OSID != "1" || first.HeldBy(99) {
		t.Errorf("held %+v", got)
	}
}

// TestSmallValues covers names, ranges and ordering.
func TestSmallValues(t *testing.T) {
	names := []string{Complete.String(), Partial.String(), Unavailable.String(), Endpoint{53, UDP}.String(), PortRef{Port: 80}.String()}
	if !slices.Equal(names, []string{"complete", "partial", "unavailable", "53/udp", "80"}) {
		t.Errorf("got %v", names)
	}
	ports := Range{First: 10, Last: 20}
	if !ports.Contains(10) || !ports.Contains(20) || ports.Contains(9) || ports.Contains(21) || (Range{}).Contains(0) {
		t.Error("range")
	}
	address := func(text string, port uint16) AddressPort {
		return AddressPort{Endpoint: Endpoint{port, TCP}, Addr: netip.MustParseAddr(text)}
	}
	sorted := []AddressPort{address("::1", 80), address("127.0.0.1", 90), address("127.0.0.1", 80)}
	SortAddresses(sorted)
	if sorted[0].String() != "127.0.0.1:80" || sorted[1].String() != "[::1]:80" || sorted[2].Port != 90 {
		t.Errorf("got %v", sorted)
	}
	if !(PortRef{80, UDP}).Matches(80, UDP) || (PortRef{80, UDP}).Matches(80, TCP) || TriOf(true) != Yes || TriOf(false) != No {
		t.Error("matching")
	}
	if err := NewError(CodeUsage, "message", "hint"); err.Error() != "message" || AsError(err) != err {
		t.Error("errors")
	}
}
