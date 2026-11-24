package stopper

import (
	"net/netip"
	"strconv"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

var (
	openGates = gates{native: true, docker: true, podman: true, graceful: true, families: true}
	web       = domain.Endpoint{Port: 8000, Protocol: domain.TCP}
	api       = domain.Endpoint{Port: 9000, Protocol: domain.TCP}
	dbID      = strings.Repeat("d", 64)
)

const (
	shellPID = 400
	appPID   = 600
	initPID  = 900
)

// developer builds a process started from user 1000's terminal.
func developer(pid, parent int) domain.Process {
	return domain.Process{
		ProcessID: domain.ProcessID{PID: pid, Start: strconv.Itoa(pid), UserID: "1000", Exe: "/usr/bin/app"},
		PPID:      parent, SessionID: shellPID, RealUID: "1000", Name: "app", Dir: "/home/dev/app",
		Args: []string{"app", "serve"}, Sockets: domain.Complete, TTY: domain.Yes, Tracer: domain.No,
		Caught: domain.Yes, Ignored: domain.No, Blocked: domain.No, Service: domain.No,
		Unit: domain.Unit{Name: "session.scope", User: true, Kind: domain.UnitScope}, Job: domain.Job{Known: true},
	}
}

// listener builds a socket on 127.0.0.1 held by the given processes.
func listener(endpoint domain.Endpoint, inode string, pids ...int) domain.Socket {
	socket := domain.Socket{
		ID: domain.SocketID{
			AddressPort: domain.AddressPort{Endpoint: endpoint, Addr: netip.MustParseAddr("127.0.0.1")},
			Family:      4, NetNS: "net:[1]", OSID: inode,
		},
		CreatorUID: "1000",
		Evidence:   []domain.Evidence{{Source: "proc-net", Completeness: domain.Complete}, {Source: "proc-fd", Completeness: domain.Partial}},
	}
	for _, pid := range pids {
		socket.Holders = append(socket.Holders, domain.Holder{PID: pid, FD: 3})
	}
	return socket
}

// world is a snapshot with one allowed listener on port 8000.
func world() domain.Snapshot {
	snapshot := domain.Snapshot{
		Host: domain.Host{
			SelfPID: 500, CallerUID: "1000", SystemUIDMax: 999, InContainer: domain.No, ServiceManager: domain.Yes,
			ProcessTable: domain.Complete, Ephemeral: domain.Range{First: 32768, Last: 60999},
		},
		Ports:       map[domain.Endpoint]domain.PortState{},
		Processes:   map[int]domain.Process{},
		EnginesSeen: domain.Complete,
	}
	for _, protocol := range []domain.Protocol{domain.TCP, domain.UDP} {
		snapshot.Ports[domain.Endpoint{Protocol: protocol}] = domain.PortState{Table: domain.Complete, Stable: domain.Yes}
	}
	boot := developer(1, 0)
	boot.Name, boot.Exe, boot.UserID, boot.RealUID, boot.TTY, boot.UserManager = "systemd", "/sbin/init", "0", "0", domain.No, true
	add(&snapshot, boot)
	shell := developer(shellPID, 1)
	shell.Name, shell.Exe, shell.Args = "bash", "/bin/bash", []string{"bash"}
	add(&snapshot, shell)
	self := developer(500, shellPID)
	self.Name, self.Exe, self.Args = "portly", "/usr/bin/portly", []string{"portly"}
	add(&snapshot, self)
	add(&snapshot, developer(appPID, shellPID))
	snapshot.Sockets = append(snapshot.Sockets, listener(web, "77", appPID))
	return snapshot
}

// add stores a process.
func add(snapshot *domain.Snapshot, process domain.Process) {
	snapshot.Processes[process.PID] = process
}

// change edits one stored process.
func change(snapshot *domain.Snapshot, pid int, edit func(*domain.Process)) {
	process := snapshot.Processes[pid]
	edit(&process)
	snapshot.Processes[pid] = process
}

// published adds a proven local Docker container publishing port 8000.
func published(snapshot *domain.Snapshot) {
	init := developer(initPID, 1)
	init.UserID, init.ContainerIDs = "0", []string{dbID}
	add(snapshot, init)
	container := domain.Container{
		ID: dbID, Engine: "engine-1", StartedAt: "t1", Name: "db", StopSignal: "SIGTERM", InitPID: initPID,
		Inspected: true, Running: true,
		Ports: []domain.Binding{{HostAddr: netip.MustParseAddr("127.0.0.1"), HostPort: 8000, ContainerPort: 80, Protocol: domain.TCP}},
	}
	snapshot.Engines = []domain.Engine{{ID: "engine-1", Kind: "docker", OwnerUID: "0", Answer: domain.AnswerOK, Containers: []domain.Container{container}}}
	proxy := listener(web, "77")
	proxy.CreatorUID = "0"
	proxy.Evidence[1].Completeness = domain.Unavailable
	snapshot.Sockets = []domain.Socket{proxy}
}

// editContainer changes the published container.
func editContainer(snapshot *domain.Snapshot, edit func(*domain.Container)) {
	edit(&snapshot.Engines[0].Containers[0])
}

// ask decides for endpoints with every gate open.
func ask(snapshot domain.Snapshot, endpoints ...domain.Endpoint) domain.StopPlan {
	return decide(snapshot, domain.StopRequest{Endpoints: endpoints}, openGates)
}

// codeOf is the refusal of the first endpoint, empty when allowed.
func codeOf(plan domain.StopPlan) domain.Code { return plan.Results[0].Refusal.Code }
