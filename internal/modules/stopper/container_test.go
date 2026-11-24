package stopper

import (
	"net/netip"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// TestPublishedContainerWins targets the container, never its proxy.
func TestPublishedContainerWins(t *testing.T) {
	snapshot := world()
	published(&snapshot)
	editContainer(&snapshot, func(c *domain.Container) {
		c.Paused, c.AutoRemove, c.SharesNet = true, true, []string{"sidecar"}
		c.Ports = append(c.Ports, domain.Binding{HostAddr: anyAddress, HostPort: 9000, ContainerPort: 90, Protocol: domain.TCP})
	})
	plan := ask(snapshot, web)
	if len(plan.Targets) != 1 {
		t.Fatalf("refused with %q", codeOf(plan))
	}
	target := plan.Targets[0]
	if target.Kind != domain.KindContainer || target.Container.ID != dbID || target.Root.PID != 0 || len(target.Members) != 0 {
		t.Errorf("got %+v", target)
	}
	if !target.Paused || !target.AutoRemove || len(target.SharesNet) != 1 || len(target.Other) != 1 || target.Other[0].Port != 9000 {
		t.Errorf("affected: %+v", target)
	}
}

// TestContainerWithoutHostSocket needs a proven engine.
func TestContainerWithoutHostSocket(t *testing.T) {
	snapshot := world()
	published(&snapshot)
	snapshot.Sockets = nil
	if plan := ask(snapshot, web); len(plan.Targets) != 1 {
		t.Errorf("refused with %q", codeOf(plan))
	}
	delete(snapshot.Processes, initPID)
	if got := codeOf(ask(snapshot, web)); got != domain.CodeContainerUnproven {
		t.Errorf("unproven: %q", got)
	}
}

// TestContainerRefusals has one case per container rule.
func TestContainerRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(*domain.Snapshot)
		want domain.Code
	}{
		{"engine forwarded from elsewhere", func(s *domain.Snapshot) { delete(s.Processes, initPID) }, domain.CodeAmbiguous},
		{"init pid belongs to something else", func(s *domain.Snapshot) {
			change(s, initPID, func(p *domain.Process) { p.ContainerIDs = nil })
		}, domain.CodeAmbiguous},
		{"init pid is 1", func(s *domain.Snapshot) {
			editContainer(s, func(c *domain.Container) { c.InitPID = 1 })
			change(s, 1, func(p *domain.Process) { p.ContainerIDs = []string{dbID} })
		}, domain.CodeAmbiguous},
		{"root holder that is not an engine", func(s *domain.Snapshot) {
			change(s, initPID, func(p *domain.Process) { p.Name, p.Exe, p.Args = "nginx", "/usr/sbin/nginx", []string{"nginx"} })
			s.Sockets[0].Holders = []domain.Holder{{PID: initPID, FD: 3}}
		}, domain.CodeAmbiguous},
		{"socket of another protocol binding", func(s *domain.Snapshot) {
			editContainer(s, func(c *domain.Container) {
				udp := c.Ports[0]
				udp.Protocol = domain.UDP
				c.Ports = []domain.Binding{udp, {HostAddr: anyAddress, HostPort: 8000, Protocol: domain.TCP}}
			})
		}, domain.CodeAmbiguous},
		{"native socket on another address", func(s *domain.Snapshot) {
			other := listener(web, "90", appPID)
			other.ID.Addr = netip.MustParseAddr("192.168.1.5")
			s.Sockets = append(s.Sockets, other)
		}, domain.CodeAmbiguous},
		{"native socket on the same address", func(s *domain.Snapshot) { s.Sockets = append(s.Sockets, listener(web, "90", appPID)) }, domain.CodeAmbiguous},
		{"proxy of another user", func(s *domain.Snapshot) { s.Sockets[0].CreatorUID = "1001" }, domain.CodeAmbiguous},
		{"engine owner unknown", func(s *domain.Snapshot) { s.Engines[0].OwnerUID = "" }, domain.CodeAmbiguous},
		{"holder is not the engine", func(s *domain.Snapshot) {
			s.Sockets[0].Holders = []domain.Holder{{PID: appPID, FD: 3}}
		}, domain.CodeAmbiguous},
		{"holder only named like a proxy", func(s *domain.Snapshot) {
			change(s, appPID, func(p *domain.Process) { p.Name = "docker-proxy" })
			s.Sockets[0].Holders = []domain.Holder{{PID: appPID, FD: 3}}
		}, domain.CodeAmbiguous},
		{"other address", func(s *domain.Snapshot) {
			editContainer(s, func(c *domain.Container) { c.Ports[0].HostAddr = netip.MustParseAddr("127.0.0.2") })
		}, domain.CodeAmbiguous},
		{"two containers", func(s *domain.Snapshot) {
			second := s.Engines[0].Containers[0]
			second.ID = "other"
			s.Engines[0].Containers = append(s.Engines[0].Containers, second)
		}, domain.CodeAmbiguous},
		{"own container", func(s *domain.Snapshot) { s.Host.SelfContainer = dbID }, domain.CodeContainerSelf},
		{"own container unknown", func(s *domain.Snapshot) { s.Host.InContainer = domain.Yes }, domain.CodeContainerSelf},
		{"may be in a container", func(s *domain.Snapshot) { s.Host.InContainer = domain.Unknown }, domain.CodeContainerSelf},
		{"podman", func(s *domain.Snapshot) { s.Engines[0].Kind = "podman" }, domain.CodeEngineRejected},
		{"unknown engine", func(s *domain.Snapshot) { s.Engines[0].Kind = "" }, domain.CodeEngineRejected},
		{"swarm", func(s *domain.Snapshot) { editContainer(s, func(c *domain.Container) { c.Orchestrator = "swarm" }) }, domain.CodeContainerManaged},
		{"not inspected", func(s *domain.Snapshot) { editContainer(s, func(c *domain.Container) { c.Inspected = false }) }, domain.CodeEngineUnresponsive},
		{"start unknown", func(s *domain.Snapshot) { editContainer(s, func(c *domain.Container) { c.StartedAt = "" }) }, domain.CodeEngineUnresponsive},
		{"engine timed out", func(s *domain.Snapshot) { s.Engines[0].Answer = domain.AnswerTimeout }, domain.CodeEngineUnresponsive},
		{"engines not checked", func(s *domain.Snapshot) { s.EnginesSeen = domain.Partial }, domain.CodeEngineUnresponsive},
		{"kill stop signal", func(s *domain.Snapshot) { editContainer(s, func(c *domain.Container) { c.StopSignal = "SIGKILL" }) }, domain.CodeNoGraceful},
		{"stop stop signal", func(s *domain.Snapshot) { editContainer(s, func(c *domain.Container) { c.StopSignal = "19" }) }, domain.CodeNoGraceful},
		{"unstable", func(s *domain.Snapshot) { s.Ports[web] = domain.PortState{Table: domain.Complete, Stable: domain.No} }, domain.CodeEvidenceUnstable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snapshot := world()
			published(&snapshot)
			c.edit(&snapshot)
			if plan := ask(snapshot, web); codeOf(plan) != c.want || len(plan.Targets) != 0 {
				t.Errorf("got %q, want %q", codeOf(plan), c.want)
			}
		})
	}
}

// TestVisibleProxyIsExplained covers root and rootless engines.
func TestVisibleProxyIsExplained(t *testing.T) {
	snapshot := world()
	published(&snapshot)
	proxy := developer(950, 1)
	proxy.UserID, proxy.RealUID, proxy.Exe = "0", "0", "/usr/bin/docker-proxy"
	add(&snapshot, proxy)
	snapshot.Sockets[0].Holders = []domain.Holder{{PID: 950, FD: 4}}
	if plan := ask(snapshot, web); len(plan.Targets) != 1 || plan.Targets[0].Kind != domain.KindContainer {
		t.Errorf("refused with %q", codeOf(plan))
	}
	// The same proxy explains nothing for an engine that may be elsewhere
	delete(snapshot.Processes, initPID)
	if got := codeOf(ask(snapshot, web)); got != domain.CodeAmbiguous {
		t.Errorf("proxy of an unproven engine: %q", got)
	}
}

// TestVMEngineNeedsItsOwnForwarder covers engines that can't be proven local.
func TestVMEngineNeedsItsOwnForwarder(t *testing.T) {
	cases := []struct {
		socket, holder string
		want           domain.Code
	}{
		{"/Users/dev/.docker/run/docker.sock", "com.docker.backend", ""},
		{"/home/dev/.docker/desktop/docker.sock", "com.docker.backend", ""},
		{"/Users/dev/.orbstack/run/docker.sock", "OrbStack Helper", ""},
		{"/Users/dev/.colima/default/docker.sock", "limactl", ""},
		{"/Users/dev/.docker/run/docker.sock", "gvproxy", domain.CodeAmbiguous},
		{"/Users/dev/.docker/run/docker.sock", "rootlessport", domain.CodeAmbiguous},
		{"/tmp/forwarded.sock", "com.docker.backend", domain.CodeAmbiguous},
		{"/tmp/forwarded.sock", "rootlessport", domain.CodeAmbiguous},
	}
	for _, c := range cases {
		snapshot := world()
		published(&snapshot)
		delete(snapshot.Processes, initPID)
		snapshot.Engines[0].ID, snapshot.Engines[0].OwnerUID = c.socket, "1000"
		change(&snapshot, appPID, func(p *domain.Process) { p.Name, p.Exe, p.Args = c.holder, "/x/"+c.holder, nil })
		snapshot.Sockets = []domain.Socket{listener(web, "77", appPID)}
		if got := codeOf(ask(snapshot, web)); got != c.want {
			t.Errorf("%s through %s: got %q, want %q", c.socket, c.holder, got, c.want)
		}
	}
}

// TestSilentEngineBlocksPublishedPorts covers a second engine of the same user.
func TestSilentEngineBlocksPublishedPorts(t *testing.T) {
	for owner, want := range map[string]domain.Code{"0": domain.CodeEngineUnresponsive, "": domain.CodeEngineUnresponsive, "1000": ""} {
		snapshot := world()
		published(&snapshot)
		snapshot.Engines = append(snapshot.Engines, domain.Engine{ID: "silent", OwnerUID: owner, Answer: domain.AnswerTimeout})
		if got := codeOf(ask(snapshot, web)); got != want {
			t.Errorf("owner %q: %q", owner, got)
		}
	}
}

// TestClosedEngineDoesNotBlockAProvenContainer covers Docker next to root's Podman.
func TestClosedEngineDoesNotBlockAProvenContainer(t *testing.T) {
	build := func(answer domain.Answer) domain.Snapshot {
		snapshot := world()
		published(&snapshot)
		snapshot.Engines = append(snapshot.Engines, domain.Engine{ID: "/run/podman/podman.sock", OwnerUID: "0", Answer: answer})
		return snapshot
	}
	if got := codeOf(ask(build(domain.AnswerDenied), web)); got != "" {
		t.Errorf("closed: %q", got)
	}
	for _, answer := range []domain.Answer{domain.AnswerTimeout, domain.AnswerMalformed, domain.AnswerUnobserved} {
		if got := codeOf(ask(build(answer), web)); got != domain.CodeEngineUnresponsive {
			t.Errorf("%v: %q", answer, got)
		}
	}
	unproven := build(domain.AnswerDenied)
	delete(unproven.Processes, initPID)
	if got := codeOf(ask(unproven, web)); got != domain.CodeEngineUnresponsive {
		t.Errorf("unproven next to a closed engine: %q", got)
	}
}

// TestSameContainerFromTwoSocketsIsOne covers two paths to one engine.
func TestSameContainerFromTwoSocketsIsOne(t *testing.T) {
	snapshot := world()
	published(&snapshot)
	second := snapshot.Engines[0]
	second.ID = "engine-2"
	snapshot.Engines = append(snapshot.Engines, second)
	plan := ask(snapshot, web)
	if len(plan.Targets) != 1 || plan.Targets[0].Container.Engine != "engine-1" {
		t.Errorf("got %+v, %q", plan.Targets, codeOf(plan))
	}
}

// TestHostNetworkReach shows every endpoint and process of the container.
func TestHostNetworkReach(t *testing.T) {
	snapshot := world()
	published(&snapshot)
	editContainer(&snapshot, func(c *domain.Container) { c.Ports, c.HostNet = nil, true })
	change(&snapshot, appPID, func(p *domain.Process) { p.ContainerIDs, p.ContainerPath = []string{dbID}, dbPath })
	snapshot.Sockets = []domain.Socket{listener(web, "77", appPID)}
	before := ask(snapshot, web)
	snapshot.Sockets = append(snapshot.Sockets, listener(api, "78", appPID))
	after := ask(snapshot, web)
	if len(after.Targets) != 1 || len(after.Targets[0].Other) != 1 || after.Targets[0].Other[0].Port != 9000 {
		t.Fatalf("got %+v", after.Targets)
	}
	if len(after.Targets[0].Children) != 2 || after.Targets[0].Children[0].PID != appPID || before.ID == after.ID {
		t.Errorf("children %+v, ids %s %s", after.Targets[0].Children, before.ID, after.ID)
	}
}

// TestHostNetworkContainer is matched through the holder's cgroup.
func TestHostNetworkContainer(t *testing.T) {
	build := func() domain.Snapshot {
		snapshot := world()
		published(&snapshot)
		editContainer(&snapshot, func(c *domain.Container) { c.Ports, c.HostNet = nil, true })
		change(&snapshot, appPID, func(p *domain.Process) { p.ContainerIDs, p.ContainerPath = []string{dbID}, dbPath })
		snapshot.Sockets = []domain.Socket{listener(web, "77", appPID)}
		return snapshot
	}
	plan := ask(build(), web)
	if len(plan.Targets) != 1 || plan.Targets[0].Kind != domain.KindContainer || len(plan.Targets[0].Named) != 1 {
		t.Fatalf("got %+v, %q", plan.Targets, codeOf(plan))
	}
	for name, edit := range map[string]func(*domain.Snapshot){
		"unproven":     func(s *domain.Snapshot) { delete(s.Processes, initPID) },
		"not host net": func(s *domain.Snapshot) { editContainer(s, func(c *domain.Container) { c.HostNet = false }) },
		"other container": func(s *domain.Snapshot) {
			change(s, appPID, func(p *domain.Process) { p.ContainerIDs = []string{"x"} })
		},
	} {
		snapshot := build()
		edit(&snapshot)
		if got := codeOf(ask(snapshot, web)); got != domain.CodeContainerUnproven {
			t.Errorf("%s: %q", name, got)
		}
	}
	mixed := build()
	add(&mixed, developer(700, shellPID))
	mixed.Sockets = append(mixed.Sockets, listener(web, "78", 700))
	if got := codeOf(ask(mixed, web)); got != domain.CodeAmbiguous {
		t.Errorf("native plus container: %q", got)
	}
}

// TestRefusedContainerIsNotStoppedThroughAnotherPort covers both mappings.
func TestRefusedContainerIsNotStoppedThroughAnotherPort(t *testing.T) {
	snapshot := world()
	published(&snapshot)
	editContainer(&snapshot, func(c *domain.Container) {
		c.Ports = append(c.Ports, domain.Binding{HostAddr: anyAddress, HostPort: 9000, ContainerPort: 90, Protocol: domain.TCP})
	})
	snapshot.Sockets = append(snapshot.Sockets, listener(api, "91", appPID))
	plan := ask(snapshot, web, api)
	if plan.Results[0].Refusal.Code != domain.CodeRefusedElsewhere || plan.Results[1].Refusal.Code != domain.CodeAmbiguous {
		t.Errorf("published: %+v", plan.Results)
	}
	// A host-network holder of the same container on a refused port
	editContainer(&snapshot, func(c *domain.Container) { c.Ports, c.HostNet = c.Ports[:1], true })
	change(&snapshot, appPID, func(p *domain.Process) { p.ContainerIDs, p.ContainerPath = []string{dbID}, dbPath })
	add(&snapshot, developer(700, shellPID))
	snapshot.Sockets[1] = listener(api, "91", appPID, 700)
	plan = ask(snapshot, web, api)
	if plan.Results[0].Refusal.Code != domain.CodeRefusedElsewhere || len(plan.Targets) != 0 {
		t.Errorf("host network: %+v", plan.Results)
	}
}

// TestContainerOnTwoPortsIsOneTarget signals it once.
func TestContainerOnTwoPortsIsOneTarget(t *testing.T) {
	snapshot := world()
	published(&snapshot)
	editContainer(&snapshot, func(c *domain.Container) {
		c.Ports = append(c.Ports, domain.Binding{HostPort: 9000, ContainerPort: 90, Protocol: domain.TCP})
	})
	plan := ask(snapshot, web, api)
	if len(plan.Targets) != 1 || len(plan.Targets[0].Bindings) != 2 || len(plan.Targets[0].Other) != 0 {
		t.Errorf("got %+v", plan)
	}
}

// TestPodmanIsNeverStopped covers force too, because nothing graceful was delivered.
func TestPodmanIsNeverStopped(t *testing.T) {
	for _, kind := range []string{"podman", "", "other"} {
		snapshot := world()
		published(&snapshot)
		snapshot.Engines[0].Kind = kind
		plan := decide(snapshot, domain.StopRequest{Endpoints: []domain.Endpoint{web}, Force: true}, openGates)
		if codeOf(plan) != domain.CodeEngineRejected || len(plan.Targets) != 0 {
			t.Errorf("%q: %+v", kind, plan)
		}
	}
}

// TestForcedContainerWithoutGracefulSignal is allowed only with force.
func TestForcedContainerWithoutGracefulSignal(t *testing.T) {
	snapshot := world()
	published(&snapshot)
	editContainer(&snapshot, func(c *domain.Container) { c.StopSignal = "kill" })
	plan := decide(snapshot, domain.StopRequest{Endpoints: []domain.Endpoint{web}, Force: true}, openGates)
	if len(plan.Targets) != 1 || plan.Targets[0].GracefulAvailable {
		t.Errorf("got %+v", plan)
	}
}

// TestGracefulSignals covers names, numbers and defaults.
func TestGracefulSignals(t *testing.T) {
	for signal, want := range map[string]bool{
		"": true, "SIGTERM": true, "15": true, "015": true, "SIGINT": true, "SIGQUIT": true, "SIGRTMIN+3": true, "RTMAX-1": true, "SIGWINCH": true,
		"SIGKILL": false, "kill": false, "9": false, "09": false, "+9": false, " 9 ": false, "0x9": false, " SIGKILL ": false,
		"SIGSTOP": false, "19": false, "019": false, "17": false, "0": false, "-1": false, "SIGSOMETHING": false, "RTMIN+999": false,
	} {
		if got := hasGracefulSignal(signal); got != want {
			t.Errorf("%q: %v", signal, got)
		}
	}
}
