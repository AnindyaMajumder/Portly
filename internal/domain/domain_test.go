package domain

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"slices"
	"testing"
)

// TestParsePortRef covers accepted and rejected port arguments.
func TestParsePortRef(t *testing.T) {
	valid := map[string]PortRef{
		"8000": {8000, ""}, ":8000": {8000, ""}, "53/udp": {53, UDP}, "80/TCP": {80, TCP}, "65535": {65535, ""}, "1": {1, ""},
	}
	for text, want := range valid {
		if got, err := ParsePortRef(text); err != nil || got != want {
			t.Errorf("ParsePortRef(%q) = %v, %v", text, got, err)
		}
	}
	for _, text := range []string{"", "0", "65536", "-1", "+80", "abc", "80/sctp", "80/", "8 0", "::80", "8000/udp/x"} {
		if _, err := ParsePortRef(text); err == nil || AsError(err).Code != CodeUsage {
			t.Errorf("ParsePortRef(%q) should be a usage error, got %v", text, err)
		}
	}
}

// FuzzParsePortRef checks that accepted text round-trips.
func FuzzParsePortRef(f *testing.F) {
	f.Add("8000")
	f.Add(":53/udp")
	f.Fuzz(func(t *testing.T, text string) {
		ref, err := ParsePortRef(text)
		if err != nil {
			return
		}
		if again, err := ParsePortRef(ref.String()); err != nil || again != ref {
			t.Fatalf("%q does not round-trip: %v %v", text, again, err)
		}
	})
}

// TestBarePortMeansTCP proves UDP is only stopped when named.
func TestBarePortMeansTCP(t *testing.T) {
	for ref, want := range map[PortRef]Protocol{{80, ""}: TCP, {80, TCP}: TCP, {80, UDP}: UDP} {
		if got := ref.StopEndpoint().Protocol; got != want {
			t.Errorf("%v stops %s, want %s", ref, got, want)
		}
	}
}

// TestProcessIdentity proves any change or unknown field breaks a match.
func TestProcessIdentity(t *testing.T) {
	base := ProcessID{PID: 10, Start: "5", UserID: "1000", Exe: "/bin/a"}
	if !base.Same(base) {
		t.Fatal("a process should match itself")
	}
	for name, change := range map[string]func(*ProcessID){
		"reused pid":    func(p *ProcessID) { p.Start = "6" },
		"exec":          func(p *ProcessID) { p.Exe = "/bin/b" },
		"setuid":        func(p *ProcessID) { p.UserID = "0" },
		"other pid":     func(p *ProcessID) { p.PID = 11 },
		"unknown pid":   func(p *ProcessID) { p.PID = 0 },
		"unknown start": func(p *ProcessID) { p.Start = "" },
		"zero start":    func(p *ProcessID) { p.Start = "0" },
		"unknown user":  func(p *ProcessID) { p.UserID = "" },
		"unknown exe":   func(p *ProcessID) { p.Exe = "" },
	} {
		changed := base
		change(&changed)
		if base.Same(changed) {
			t.Errorf("%s should differ", name)
		}
	}
	for name, change := range map[string]func(*ProcessID){
		"start": func(p *ProcessID) { p.Start = "" },
		"user":  func(p *ProcessID) { p.UserID = "" },
		"exe":   func(p *ProcessID) { p.Exe = "" },
	} {
		unknown := base
		change(&unknown)
		if unknown.Same(unknown) {
			t.Errorf("unknown %s must not match itself", name)
		}
	}
}

// testSocket builds a fully known socket identity.
func testSocket(inode string) SocketID {
	endpoint := Endpoint{Port: 8000, Protocol: TCP}
	return SocketID{
		AddressPort: AddressPort{Endpoint: endpoint, Addr: netip.MustParseAddr("127.0.0.1")},
		Family:      4, NetNS: "net:[1]", OSID: inode,
	}
}

// TestSocketIdentity proves unknown sockets never match.
func TestSocketIdentity(t *testing.T) {
	base := testSocket("77")
	if !base.Same(base) {
		t.Fatal("a socket should match itself")
	}
	if base.Same(testSocket("78")) {
		t.Error("another inode should differ")
	}
	for _, unknown := range []SocketID{testSocket(""), testSocket("0"), {}} {
		if unknown.Same(unknown) {
			t.Errorf("%+v must not match itself", unknown)
		}
	}
	noNamespace := base
	noNamespace.NetNS = ""
	if noNamespace.Same(noNamespace) {
		t.Error("unknown namespace must not match")
	}
}

// TestSocketCompleteness proves the weakest evidence decides.
func TestSocketCompleteness(t *testing.T) {
	cases := []struct {
		evidence []Evidence
		want     Completeness
	}{
		{nil, Unavailable},
		{[]Evidence{{"a", Complete}}, Complete},
		{[]Evidence{{"a", Complete}, {"b", Partial}}, Partial},
		{[]Evidence{{"a", Complete}, {"b", Unavailable}}, Unavailable},
	}
	for _, c := range cases {
		if got := (Socket{Evidence: c.evidence}).Completeness(); got != c.want {
			t.Errorf("%v: got %v, want %v", c.evidence, got, c.want)
		}
	}
}

// TestAncestorsAndDescendants covers chains, cycles and missing parents.
func TestAncestorsAndDescendants(t *testing.T) {
	snapshot := Snapshot{Processes: map[int]Process{}}
	for pid, parent := range map[int]int{1: 0, 10: 1, 11: 10, 12: 11, 20: 99, 30: 31, 31: 30} {
		snapshot.Processes[pid] = Process{ProcessID: ProcessID{PID: pid}, PPID: parent}
	}
	if chain, ok := snapshot.Ancestors(12); !ok || len(chain) != 3 {
		t.Errorf("got %d ancestors, %v", len(chain), ok)
	}
	for _, broken := range []int{20, 30, 404} {
		if _, ok := snapshot.Ancestors(broken); ok {
			t.Errorf("chain of %d should be reported broken", broken)
		}
	}
	if got := snapshot.Descendants(10); !slices.Equal(got, []int{11, 12}) {
		t.Errorf("descendants: %v", got)
	}
}

// testTarget builds an allowed single-process target.
func testTarget() StopTarget {
	return StopTarget{
		Kind: KindProcess, Root: ProcessID{PID: 10, Start: "5", UserID: "1000", Exe: "/bin/a"},
		Members: []ProcessID{{PID: 10, Start: "5", UserID: "1000", Exe: "/bin/a"}},
		Named:   []SocketID{testSocket("77")}, GracefulAvailable: true,
	}
}

// TestApproveAllowsTheSameTarget proves an approval works once, for one method.
func TestApproveAllowsTheSameTarget(t *testing.T) {
	approval, err := Approve(testTarget(), testTarget(), Graceful)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approval.Take(Forced); err == nil {
		t.Error("a graceful approval must not force")
	}
	if _, err := approval.Take(Graceful); err != nil {
		t.Errorf("first use: %v", err)
	}
	if _, err := approval.Take(Graceful); err == nil {
		t.Error("an approval must work once")
	}
}

// TestZeroApprovalRefuses proves nothing acts without an approval.
func TestZeroApprovalRefuses(t *testing.T) {
	for _, approval := range []*Approval{nil, {}} {
		for _, method := range []Method{"", Graceful, Forced} {
			if _, err := approval.Take(method); err == nil {
				t.Errorf("zero approval allowed %q", method)
			}
		}
	}
	if _, err := Approve(testTarget(), testTarget(), ""); err == nil {
		t.Error("an empty method was approved")
	}
	if _, err := Approve(StopTarget{}, StopTarget{}, Graceful); err == nil {
		t.Error("an empty target was approved")
	}
}

// TestApproveRefusesChanges covers every change between prompt and signal.
func TestApproveRefusesChanges(t *testing.T) {
	extra := AddressPort{Endpoint: Endpoint{9000, TCP}, Addr: netip.MustParseAddr("127.0.0.1")}
	changes := map[string]func(*StopTarget){
		"process replaced": func(s *StopTarget) { s.Root.Start = "6" },
		"socket replaced":  func(s *StopTarget) { s.Named = []SocketID{testSocket("78")} },
		"socket closed":    func(s *StopTarget) { s.Named = nil },
		"endpoint gained":  func(s *StopTarget) { s.Other = []AddressPort{extra} },
		"suspended":        func(s *StopTarget) { s.Suspended = true },
		"paused":           func(s *StopTarget) { s.Paused = true },
		"kind":             func(s *StopTarget) { s.Kind = KindFamily },
	}
	for name, change := range changes {
		current := testTarget()
		change(&current)
		if _, err := Approve(testTarget(), current, Graceful); err == nil || AsError(err).Code != CodeChanged {
			t.Errorf("%s: got %v, want target_changed", name, err)
		}
	}
}

// TestForceApprovalComparesIdentityOnly allows closed sockets but not all of them.
func TestForceApprovalComparesIdentityOnly(t *testing.T) {
	planned := testTarget()
	planned.Named = append(planned.Named, testSocket("78"))
	current := testTarget()
	if _, err := Approve(planned, current, Forced); err != nil {
		t.Errorf("one remaining socket should be enough: %v", err)
	}
	current.Named = nil
	if _, err := Approve(planned, current, Forced); err == nil {
		t.Error("no remaining socket must refuse")
	}
}

// TestFirstForceApprovalChecksReach covers targets without a graceful method.
func TestFirstForceApprovalChecksReach(t *testing.T) {
	planned := testTarget()
	planned.GracefulAvailable = false
	current := planned
	if _, err := Approve(planned, current, Forced); err != nil {
		t.Fatal(err)
	}
	current.Suspended = true
	if _, err := Approve(planned, current, Forced); err == nil {
		t.Error("a change since the only prompt must refuse")
	}
}

// TestContainerApproval proves a restarted container is another target.
func TestContainerApproval(t *testing.T) {
	planned := StopTarget{Kind: KindContainer, Container: ContainerRef{"e", "id", "t1"}}
	if _, err := Approve(planned, planned, Graceful); err != nil {
		t.Fatal(err)
	}
	restarted := planned
	restarted.Container.StartedAt = "t2"
	if _, err := Approve(planned, restarted, Graceful); err == nil {
		t.Error("a restarted container must refuse")
	}
	unknown := StopTarget{Kind: KindContainer}
	if _, err := Approve(unknown, unknown, Graceful); err == nil {
		t.Error("an unknown container must refuse")
	}
}

// TestCodesMatchSchema keeps reason codes and the JSON schema equal.
func TestCodesMatchSchema(t *testing.T) {
	data, err := os.ReadFile("../../schema/portly.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			Code struct{ Enum []Code }
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	want, got := schema.Defs.Code.Enum, Codes()
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("codes and schema differ:\n%v\n%v", got, want)
	}
}

// TestExitCodes pins the documented exit statuses.
func TestExitCodes(t *testing.T) {
	for code, want := range map[Code]int{
		CodeUsage: 2, CodeNotListening: 3, CodePermissionDenied: 4, CodeProtected: 5,
		CodePlatform: 5, CodeCancelled: 6, CodeRestarted: 7, "new": 1,
	} {
		if got := code.ExitCode(); got != want {
			t.Errorf("%s exits %d, want %d", code, got, want)
		}
	}
	if AsError(errors.New("x")).Code != CodeInternal {
		t.Error("unknown errors are internal")
	}
}

// TestPortStateFallsBackToTheTable covers ports without sockets.
func TestPortStateFallsBackToTheTable(t *testing.T) {
	endpoint := Endpoint{8000, TCP}
	snapshot := Snapshot{Ports: map[Endpoint]PortState{{0, TCP}: {Table: Complete}}}
	if got := snapshot.PortState(endpoint).Table; got != Complete {
		t.Errorf("got %v", got)
	}
	snapshot.Ports[endpoint] = PortState{Table: Partial}
	if got := snapshot.PortState(endpoint).Table; got != Partial {
		t.Errorf("got %v", got)
	}
	if got := snapshot.PortState(Endpoint{53, UDP}).Table; got != Unavailable {
		t.Errorf("an unread table must be unavailable, got %v", got)
	}
}

// TestForcedFamilyMayShrinkOnly covers workers that exit or appear.
func TestForcedFamilyMayShrinkOnly(t *testing.T) {
	worker := ProcessID{PID: 11, Start: "6", UserID: "1000", Exe: "/bin/a"}
	planned := testTarget()
	planned.Kind, planned.Members = KindFamily, append(planned.Members, worker)
	if _, err := Approve(planned, testTarget(), Forced); err != nil {
		t.Errorf("a smaller family is still the target: %v", err)
	}
	if _, err := Approve(planned, testTarget(), Graceful); err == nil {
		t.Error("a graceful stop needs the same kind")
	}
	if _, err := Approve(testTarget(), planned, Forced); err == nil {
		t.Error("a grown family must refuse")
	}
	empty := testTarget()
	empty.Members = nil
	if _, err := Approve(planned, empty, Forced); err == nil {
		t.Error("no members must refuse")
	}
	container := StopTarget{Kind: KindContainer, Container: ContainerRef{"e", "id", "t1"}}
	if _, err := Approve(container, planned, Forced); err == nil {
		t.Error("a container is never a process")
	}
}
