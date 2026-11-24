package stopper

import (
	"math/rand/v2"
	"net/netip"
	"reflect"
	"slices"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// TestAllowsOneResolvedOwner covers the plain case and what it shows.
func TestAllowsOneResolvedOwner(t *testing.T) {
	snapshot := world()
	add(&snapshot, developer(601, appPID))
	snapshot.Sockets = append(snapshot.Sockets,
		listener(api, "78", appPID), listener(domain.Endpoint{Port: 9001, Protocol: domain.TCP}, "79", 601),
		listener(domain.Endpoint{Port: 5353, Protocol: domain.UDP}, "80", appPID),
		listener(domain.Endpoint{Port: 40000, Protocol: domain.UDP}, "81", appPID))
	plan := ask(snapshot, web)
	if len(plan.Targets) != 1 || plan.Results[0].Target != 0 || plan.ID == "" {
		t.Fatalf("got %+v", plan)
	}
	target := plan.Targets[0]
	if target.Kind != domain.KindProcess || target.Root.PID != appPID || len(target.Named) != 1 || !target.GracefulAvailable {
		t.Errorf("got %+v", target)
	}
	var others []uint16
	for _, other := range target.Other {
		others = append(others, other.Port)
	}
	if !slices.Equal(others, []uint16{5353, 9000, 9001}) {
		t.Errorf("affected ports %v", others)
	}
	if len(target.Children) != 1 || target.Children[0].PID != 601 {
		t.Errorf("children %+v", target.Children)
	}
}

// refusalCases has one change per policy rule.
var refusalCases = []struct {
	name   string
	change func(*domain.Snapshot)
	want   domain.Code
}{
	{"table unreadable", func(s *domain.Snapshot) { s.Ports = nil }, domain.CodeEvidenceUnavailable},
	{"table partial", func(s *domain.Snapshot) { s.Ports[web] = domain.PortState{Table: domain.Partial, Stable: domain.Yes} }, domain.CodeEvidenceUnavailable},
	{"still changing", func(s *domain.Snapshot) { s.Ports[web] = domain.PortState{Table: domain.Complete, Stable: domain.No} }, domain.CodeEvidenceUnstable},
	{"stability unknown", func(s *domain.Snapshot) { s.Ports[web] = domain.PortState{Table: domain.Complete} }, domain.CodeEvidenceUnstable},
	{"nothing listens", func(s *domain.Snapshot) { s.Sockets = nil }, domain.CodeNotListening},
	{"inode unknown", func(s *domain.Snapshot) { s.Sockets[0].ID.OSID = "0" }, domain.CodeEvidenceUnavailable},
	{"namespace unknown", func(s *domain.Snapshot) { s.Sockets[0].ID.NetNS = "" }, domain.CodeEvidenceUnavailable},
	{"own ownerless socket", func(s *domain.Snapshot) { s.Sockets[0].Holders = nil }, domain.CodeUnresolved},
	{"root ownerless socket", func(s *domain.Snapshot) { s.Sockets[0].Holders = nil; s.Sockets[0].CreatorUID = "0" }, domain.CodePermissionDenied},
	{"second socket ownerless", func(s *domain.Snapshot) { s.Sockets = append(s.Sockets, listener(web, "78")) }, domain.CodeUnresolved},
	{"creator not among holders", func(s *domain.Snapshot) { s.Sockets[0].CreatorUID = "1001" }, domain.CodeUnresolved},
	{"creator unknown", func(s *domain.Snapshot) { s.Sockets[0].CreatorUID = "" }, domain.CodeUnresolved},
	{"holder not in table", func(s *domain.Snapshot) { delete(s.Processes, appPID) }, domain.CodeUnresolved},
	{"no evidence", func(s *domain.Snapshot) { s.Sockets[0].Evidence = nil }, domain.CodeUnresolved},
	{"two owners", func(s *domain.Snapshot) {
		add(s, developer(700, shellPID))
		s.Sockets = append(s.Sockets, listener(web, "78", 700))
	}, domain.CodeAmbiguous},
	{"shared by strangers", func(s *domain.Snapshot) {
		add(s, developer(700, shellPID))
		s.Sockets[0] = listener(web, "77", appPID, 700)
	}, domain.CodeAmbiguous},
	{"pid 1", func(s *domain.Snapshot) { s.Sockets[0] = listener(web, "77", 1); s.Sockets[0].CreatorUID = "0" }, domain.CodeProtected},
	{"pid 1 of a container", func(s *domain.Snapshot) {
		change(s, 1, func(p *domain.Process) { p.UserManager, p.UserID, p.RealUID, p.TTY = false, "1000", "1000", domain.Yes })
		s.Host.SelfPID = 800
		self := developer(800, 0)
		add(s, self)
		s.Sockets[0] = listener(web, "77", 1)
	}, domain.CodeProtected},
	{"user unknown", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) { p.UserID = "" })
	}, domain.CodeUnresolved},
	{"kernel", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Kernel = true }) }, domain.CodeProtected},
	{"user manager", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.UserManager = true }) }, domain.CodeProtected},
	{"portly itself", func(s *domain.Snapshot) { s.Sockets[0] = listener(web, "77", 500) }, domain.CodeProtected},
	{"portly's ancestor", func(s *domain.Snapshot) { s.Sockets[0] = listener(web, "77", shellPID) }, domain.CodeProtected},
	{"own ancestry unknown", func(s *domain.Snapshot) { delete(s.Processes, 500) }, domain.CodeProtected},
	{"system account without manager", func(s *domain.Snapshot) {
		s.Host.ServiceManager, s.Host.Privileged = domain.No, true
		change(s, appPID, func(p *domain.Process) { p.UserID, p.RealUID, p.TTY = "33", "33", domain.No })
		s.Sockets[0].CreatorUID = "33"
	}, domain.CodeProtected},
	{"nobody without unit", func(s *domain.Snapshot) {
		s.Host.Privileged = true
		change(s, appPID, func(p *domain.Process) {
			p.UserID, p.RealUID, p.TTY, p.Unit = "65534", "65534", domain.No, domain.Unit{Kind: domain.UnitNone}
		})
		s.Sockets[0].CreatorUID = "65534"
	}, domain.CodeProtected},
	{"docker-proxy", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) { p.Exe = "/usr/bin/docker-proxy" })
	}, domain.CodeForwarder},
	{"cut comm", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Name = "com.docker.back" }) }, domain.CodeForwarder},
	{"sshd title", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) { p.Args = []string{"sshd: dev@pts/0"} })
	}, domain.CodeForwarder},
	{"vm ssh mux", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) {
			p.Name, p.Args = "ssh", []string{"ssh: /Users/dev/.lima/default/ssh.sock [mux]"}
		})
	}, domain.CodeForwarder},
	{"vm ssh by path", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) {
			p.Name, p.Args = "ssh", []string{"ssh", "-F", "/Users/dev/.colima/ssh_config"}
		})
	}, domain.CodeForwarder},
	{"vm ssh", func(s *domain.Snapshot) {
		change(s, shellPID, func(p *domain.Process) { p.Name = "limactl" })
		change(s, appPID, func(p *domain.Process) { p.Name = "ssh" })
	}, domain.CodeForwarder},
	{"ancestry unknown", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.PPID = 12345 }) }, domain.CodeUnresolved},
	{"windows service", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Service = domain.Yes }) }, domain.CodeSupervised},
	{"service unknown", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Service = domain.Unknown }) }, domain.CodeSupervised},
	{"launchd unreadable", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Job = domain.Job{} }) }, domain.CodeEvidenceUnavailable},
	{"launchd job", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) { p.Job = domain.Job{Label: "x", Known: true} })
	}, domain.CodeSupervised},
	{"launchd app itself", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) {
			p.Job = domain.Job{Label: "application.x", MainPID: appPID, Application: true, Known: true}
		})
	}, domain.CodeSupervised},
	{"systemd service", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) { p.Unit = domain.Unit{Name: "web.service", Kind: domain.UnitService} })
	}, domain.CodeSupervised},
	{"cgroup unreadable", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Unit = domain.Unit{} }) }, domain.CodeEvidenceUnavailable},
	{"other user", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) { p.UserID, p.RealUID = "1001", "1001" })
		s.Sockets[0].CreatorUID = "1001"
	}, domain.CodePermissionDenied},
	{"setuid", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.UserID = "0" }) }, domain.CodePermissionDenied},
	{"exe unknown", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Exe = "" }) }, domain.CodeUnresolved},
	{"start unknown", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Start = "" }) }, domain.CodeUnresolved},
	{"zombie", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Zombie = true }) }, domain.CodeUnresolved},
	{"debugged", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Tracer = domain.Yes }) }, domain.CodeDebugged},
	{"tracer unknown", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Tracer = domain.Unknown }) }, domain.CodeDebugged},
	{"suspended, ignores", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) { p.Suspended, p.Ignored = true, domain.Yes })
	}, domain.CodeStopBlocked},
	{"suspended, blocks", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) { p.Suspended, p.Blocked = true, domain.Unknown })
	}, domain.CodeStopBlocked},
	{"hidden processes", func(s *domain.Snapshot) { s.Host.ProcessTable = domain.Partial }, domain.CodeEvidenceUnavailable},
	{"unreadable child", func(s *domain.Snapshot) {
		child := developer(601, appPID)
		child.Sockets = domain.Partial
		add(s, child)
	}, domain.CodeDescendantUnreadable},
	{"engines not checked", func(s *domain.Snapshot) { s.EnginesSeen = domain.Unavailable }, domain.CodeEngineUnresponsive},
	{"own engine silent", func(s *domain.Snapshot) {
		s.Engines = []domain.Engine{{ID: "e", OwnerUID: "1000", Answer: domain.AnswerTimeout}}
	}, domain.CodeEngineUnresponsive},
	{"engine owner unknown", func(s *domain.Snapshot) {
		s.Engines = []domain.Engine{{ID: "e", Answer: domain.AnswerUnobserved}}
	}, domain.CodeEngineUnresponsive},
	{"container without engine", func(s *domain.Snapshot) {
		change(s, appPID, func(p *domain.Process) { p.ContainerIDs = []string{dbID} })
	}, domain.CodeContainerUnproven},
}

// TestRefusals has one case per rule, and no refusal may leave a target.
func TestRefusals(t *testing.T) {
	for _, c := range refusalCases {
		t.Run(c.name, func(t *testing.T) {
			snapshot := world()
			c.change(&snapshot)
			plan := ask(snapshot, web)
			if codeOf(plan) != c.want || len(plan.Targets) != 0 || plan.ID != "" || plan.Results[0].Target != -1 {
				t.Errorf("got %q, want %q", codeOf(plan), c.want)
			}
			forced := decide(snapshot, domain.StopRequest{Endpoints: []domain.Endpoint{web}, Force: true}, openGates)
			if codeOf(forced) != c.want {
				t.Errorf("--force changed the refusal to %q", codeOf(forced))
			}
		})
	}
}

// TestAllowedDespiteAppearances covers cases that must not be refused.
func TestAllowedDespiteAppearances(t *testing.T) {
	cases := map[string]func(*domain.Snapshot){
		"unrelated unreadable process": func(s *domain.Snapshot) {
			other := developer(800, 1)
			other.Sockets = domain.Unavailable
			add(s, other)
		},
		"zombie child": func(s *domain.Snapshot) {
			child := developer(601, appPID)
			child.Zombie, child.Sockets = true, domain.Unavailable
			add(s, child)
		},
		"root engine silent": func(s *domain.Snapshot) {
			s.Engines = []domain.Engine{{ID: "e", OwnerUID: "0", Answer: domain.AnswerTimeout}}
		},
		"engine absent": func(s *domain.Snapshot) {
			s.Engines = []domain.Engine{{ID: "e", OwnerUID: "1000", Answer: domain.AnswerAbsent}}
		},
		"unknown project": func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Dir, p.Args = "", nil }) },
		"terminal inside a unit": func(s *domain.Snapshot) {
			change(s, appPID, func(p *domain.Process) {
				p.Unit = domain.Unit{Name: "app-code.service", User: true, Kind: domain.UnitService, MainPID: 9, MainKnown: true}
			})
		},
		"launched by an application": func(s *domain.Snapshot) {
			change(s, appPID, func(p *domain.Process) {
				p.Job = domain.Job{Label: "application.x", MainPID: 9, Application: true, Known: true}
			})
		},
		"suspended":         func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Suspended = true }) },
		"no signal handler": func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Caught = domain.No }) },
		"plain ssh":         func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Name = "ssh" }) },
		"same socket twice": func(s *domain.Snapshot) { s.Sockets = append(s.Sockets, listener(web, "78", appPID)) },
	}
	for name, edit := range cases {
		snapshot := world()
		edit(&snapshot)
		if plan := ask(snapshot, web); len(plan.Targets) != 1 {
			t.Errorf("%s: refused with %q", name, codeOf(plan))
		}
	}
}

// TestUnitExceptionNeedsProof covers the terminal exception's limits.
func TestUnitExceptionNeedsProof(t *testing.T) {
	unit := domain.Unit{Name: "web.service", Kind: domain.UnitService, MainPID: 9, MainKnown: true}
	cases := map[string]func(*domain.Process){
		"main process":     func(p *domain.Process) { p.Unit.MainPID = appPID },
		"direct child":     func(p *domain.Process) { p.Unit.MainPID = shellPID },
		"main unknown":     func(p *domain.Process) { p.Unit.MainKnown = false },
		"no terminal":      func(p *domain.Process) { p.TTY = domain.No },
		"terminal unknown": func(p *domain.Process) { p.TTY = domain.Unknown },
	}
	for name, edit := range cases {
		snapshot := world()
		change(&snapshot, appPID, func(p *domain.Process) { p.Unit = unit; edit(p) })
		if got := codeOf(ask(snapshot, web)); got != domain.CodeSupervised {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

// TestUnitAdvice prints a stop command only for safe, ordinary units.
func TestUnitAdvice(t *testing.T) {
	cases := []struct {
		unit string
		main int
		want domain.Advice
	}{
		{"web.service", appPID, domain.Advice{Unit: "web.service", CanStopUnit: true}},
		{"web.service", 9, domain.Advice{Unit: "web.service"}},
		{"ssh.service", appPID, domain.Advice{Unit: "ssh.service"}},
		{"user@1000.service", appPID, domain.Advice{Unit: "user@1000.service"}},
		{"gdm.service", appPID, domain.Advice{Unit: "gdm.service"}},
		{"a;rm -rf.service", appPID, domain.Advice{}},
		{"-now.service", appPID, domain.Advice{}},
		{"$(id).service", appPID, domain.Advice{}},
	}
	for _, c := range cases {
		snapshot := world()
		change(&snapshot, appPID, func(p *domain.Process) {
			p.TTY, p.Unit = domain.No, domain.Unit{Name: c.unit, Kind: domain.UnitService, MainPID: c.main, MainKnown: true}
		})
		if got := ask(snapshot, web).Results[0].Refusal; got.Code != domain.CodeSupervised || got.Advice != c.want {
			t.Errorf("%s: got %+v", c.unit, got)
		}
	}
}

// TestSupervisors covers wrappers, scripts, titles and sessions.
func TestSupervisors(t *testing.T) {
	cases := []struct {
		name   string
		parent func(*domain.Process)
		want   domain.Code
	}{
		{"nodemon script", func(p *domain.Process) { p.Args = []string{"node", "/app/node_modules/.bin/nodemon", "a.js"} }, domain.CodeSupervised},
		{"nodemon.js", func(p *domain.Process) { p.Args = []string{"node", "/app/nodemon.js"} }, domain.CodeSupervised},
		{"pm2 title", func(p *domain.Process) { p.Args = []string{"PM2 v5.3.0: God Daemon (/home/dev/.pm2)"} }, domain.CodeSupervised},
		{"supervisord", func(p *domain.Process) { p.Args = []string{"/usr/bin/python3", "/usr/bin/supervisord"} }, domain.CodeSupervised},
		{"module", func(p *domain.Process) { p.Args = []string{"python3", "-m", "supervisor.supervisord"} }, domain.CodeSupervised},
		{"air", func(p *domain.Process) { p.Exe = "/home/dev/go/bin/air" }, domain.CodeSupervised},
		{"node watch", func(p *domain.Process) { p.Exe, p.Args = "/usr/bin/node", []string{"node", "--watch", "a.js"} }, domain.CodeSupervised},
		{"watch flag of another tool", func(p *domain.Process) { p.Args = []string{"app", "--watch"} }, ""},
		{"npm", func(p *domain.Process) { p.Args = []string{"npm", "run", "dev"} }, ""},
	}
	for _, c := range cases {
		snapshot := world()
		parent, wrapper := developer(590, shellPID), developer(595, 590)
		c.parent(&parent)
		wrapper.Exe, wrapper.Args = "/bin/sh", []string{"sh", "-c", "app"}
		add(&snapshot, parent)
		add(&snapshot, wrapper)
		change(&snapshot, appPID, func(p *domain.Process) { p.PPID = 595 })
		if got := codeOf(ask(snapshot, web)); got != c.want {
			t.Errorf("%s: got %q", c.name, got)
		}
	}
}

// TestSupervisorOutsideTheSession proves setsid does not hide a supervisor.
func TestSupervisorOutsideTheSession(t *testing.T) {
	snapshot := world()
	daemon := developer(590, 1)
	daemon.Name, daemon.TTY, daemon.SessionID = "supervisord", domain.No, 590
	add(&snapshot, daemon)
	change(&snapshot, appPID, func(p *domain.Process) { p.PPID, p.SessionID, p.TTY = 590, appPID, domain.No })
	if got := codeOf(ask(snapshot, web)); got != domain.CodeSupervised {
		t.Errorf("got %q", got)
	}
	// A supervisor above the user's own terminal session is unrelated
	change(&snapshot, appPID, func(p *domain.Process) { p.PPID, p.SessionID, p.TTY = shellPID, shellPID, domain.Yes })
	change(&snapshot, shellPID, func(p *domain.Process) { p.PPID, p.SessionID = 590, shellPID })
	if got := codeOf(ask(snapshot, web)); got != "" {
		t.Errorf("unrelated supervisor refused with %q", got)
	}
}

// family makes port 8000 a three-process prefork server.
func family() domain.Snapshot {
	snapshot := world()
	add(&snapshot, developer(601, appPID))
	add(&snapshot, developer(602, 601))
	snapshot.Sockets[0] = listener(web, "77", appPID, 601, 602)
	return snapshot
}

// TestVerifiedFamily signals only the root and shows every member.
func TestVerifiedFamily(t *testing.T) {
	plan := ask(family(), web)
	if len(plan.Targets) != 1 {
		t.Fatalf("refused with %q", codeOf(plan))
	}
	target := plan.Targets[0]
	if target.Kind != domain.KindFamily || target.Root.PID != appPID || len(target.Members) != 3 || len(target.Children) != 0 {
		t.Errorf("got %+v", target)
	}
}

// TestFamilyRefusals covers every requirement of a family.
func TestFamilyRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(*domain.Snapshot)
		want domain.Code
	}{
		{"other user", func(s *domain.Snapshot) { change(s, 602, func(p *domain.Process) { p.UserID = "1001" }) }, domain.CodeAmbiguous},
		{"other real user", func(s *domain.Snapshot) { change(s, 602, func(p *domain.Process) { p.RealUID = "1001" }) }, domain.CodeAmbiguous},
		{"other program", func(s *domain.Snapshot) { change(s, 602, func(p *domain.Process) { p.Exe = "/bin/x" }) }, domain.CodeAmbiguous},
		{"program unknown", func(s *domain.Snapshot) {
			for _, pid := range []int{appPID, 601, 602} {
				change(s, pid, func(p *domain.Process) { p.Exe = "" })
			}
		}, domain.CodeAmbiguous},
		{"no handler", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Caught = domain.No }) }, domain.CodeAmbiguous},
		{"handler unknown", func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Caught = domain.Unknown }) }, domain.CodeAmbiguous},
		{"chain through a stranger", func(s *domain.Snapshot) {
			add(s, developer(650, 601))
			change(s, 602, func(p *domain.Process) { p.PPID = 650 })
		}, domain.CodeAmbiguous},
		{"root lacks a socket", func(s *domain.Snapshot) { s.Sockets = append(s.Sockets, listener(web, "78", 601)) }, domain.CodeAmbiguous},
		{"member debugged", func(s *domain.Snapshot) { change(s, 602, func(p *domain.Process) { p.Tracer = domain.Yes }) }, domain.CodeDebugged},
		{"member forwards", func(s *domain.Snapshot) { change(s, 602, func(p *domain.Process) { p.Name = "conmon" }) }, domain.CodeForwarder},
	}
	for _, c := range cases {
		snapshot := family()
		c.edit(&snapshot)
		if got := codeOf(ask(snapshot, web)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	noFamilies := openGates
	noFamilies.families = false
	if plan := decide(family(), domain.StopRequest{Endpoints: []domain.Endpoint{web}, Force: true}, noFamilies); codeOf(plan) != domain.CodeAmbiguous {
		t.Errorf("families without support: %q", codeOf(plan))
	}
}

// TestFamilyCycleIsRefused covers holders whose parents form a loop.
func TestFamilyCycleIsRefused(t *testing.T) {
	snapshot := family()
	change(&snapshot, 601, func(p *domain.Process) { p.PPID = 602 })
	if got := codeOf(ask(snapshot, web)); got != domain.CodeAmbiguous {
		t.Errorf("got %q", got)
	}
}

// TestFamilyJoinsItsRootsOtherPort merges one program on two ports.
func TestFamilyJoinsItsRootsOtherPort(t *testing.T) {
	snapshot := family()
	snapshot.Sockets = append(snapshot.Sockets, listener(api, "78", appPID))
	for _, order := range [][]domain.Endpoint{{web, api}, {api, web}} {
		plan := ask(snapshot, order...)
		if len(plan.Targets) != 1 || plan.Targets[0].Kind != domain.KindFamily || len(plan.Targets[0].Members) != 3 {
			t.Errorf("%v: %+v", order, plan.Targets)
		}
	}
}

// TestSupervisorWalkReachesPID1 covers chains without any terminal session.
func TestSupervisorWalkReachesPID1(t *testing.T) {
	snapshot := world()
	change(&snapshot, appPID, func(p *domain.Process) { p.PPID, p.TTY = 1, domain.No })
	if got := codeOf(ask(snapshot, web)); got != "" {
		t.Errorf("orphan refused with %q", got)
	}
	change(&snapshot, appPID, func(p *domain.Process) { p.PPID = 0 })
	if got := codeOf(ask(snapshot, web)); got != "" {
		t.Errorf("no parent refused with %q", got)
	}
}

// TestSessionLeaderIsItsOwnSession needs no ancestor check.
func TestSessionLeaderIsItsOwnSession(t *testing.T) {
	snapshot := world()
	parent := developer(590, shellPID)
	parent.Name = "supervisord"
	add(&snapshot, parent)
	change(&snapshot, appPID, func(p *domain.Process) { p.PPID, p.SessionID = 590, appPID })
	if got := codeOf(ask(snapshot, web)); got != "" {
		t.Errorf("got %q", got)
	}
}

// TestPackageRunnerIsNotTheTarget proves ancestry alone means nothing.
func TestPackageRunnerIsNotTheTarget(t *testing.T) {
	snapshot := world()
	add(&snapshot, developer(590, shellPID))
	change(&snapshot, appPID, func(p *domain.Process) { p.PPID = 590 })
	if target := ask(snapshot, web).Targets[0]; target.Root.PID != appPID || target.Kind != domain.KindProcess {
		t.Errorf("got %+v", target)
	}
}

// TestReloaderIsShown marks the pair and still targets the listener.
func TestReloaderIsShown(t *testing.T) {
	snapshot := world()
	add(&snapshot, developer(590, shellPID))
	change(&snapshot, appPID, func(p *domain.Process) { p.PPID = 590 })
	if target := ask(snapshot, web).Targets[0]; !target.Reloader || target.Root.PID != appPID {
		t.Errorf("got %+v", target)
	}
	change(&snapshot, 590, func(p *domain.Process) { p.Args = []string{"app", "other"} })
	if ask(snapshot, web).Targets[0].Reloader {
		t.Error("other arguments are not a reloader")
	}
}

// TestSeveralPorts covers grouping, duplicates and independent refusals.
func TestSeveralPorts(t *testing.T) {
	snapshot := world()
	snapshot.Sockets = append(snapshot.Sockets, listener(api, "78", appPID))
	plan := ask(snapshot, web, api, web)
	if len(plan.Targets) != 1 || len(plan.Results) != 2 || len(plan.Targets[0].Named) != 2 || len(plan.Targets[0].Other) != 0 {
		t.Errorf("one process on two ports: %+v", plan)
	}
	add(&snapshot, developer(700, shellPID))
	snapshot.Sockets[1] = listener(api, "78", 700)
	if plan := ask(snapshot, web, api); len(plan.Targets) != 2 {
		t.Errorf("two processes: %+v", plan)
	}
	change(&snapshot, 700, func(p *domain.Process) { p.Tracer = domain.Yes })
	plan = ask(snapshot, web, api)
	if len(plan.Targets) != 1 || plan.Results[0].Target != 0 || plan.Results[1].Refusal.Code != domain.CodeDebugged {
		t.Errorf("a refusal must not block another target: %+v", plan)
	}
}

// TestRefusedTargetIsNotStoppedThroughAnotherPort covers native owners.
func TestRefusedTargetIsNotStoppedThroughAnotherPort(t *testing.T) {
	snapshot := world()
	add(&snapshot, developer(700, shellPID))
	third := domain.Endpoint{Port: 9100, Protocol: domain.TCP}
	snapshot.Sockets = append(snapshot.Sockets, listener(api, "78", appPID, 700), listener(third, "79", 700))
	plan := ask(snapshot, web, api, third)
	want := []domain.Code{domain.CodeRefusedElsewhere, domain.CodeAmbiguous, domain.CodeRefusedElsewhere}
	for i, result := range plan.Results {
		if result.Refusal.Code != want[i] {
			t.Errorf("%v: got %q, want %q", result.Endpoint, result.Refusal.Code, want[i])
		}
	}
	if len(plan.Targets) != 0 {
		t.Errorf("targets left: %+v", plan.Targets)
	}
}

// TestRefusedChildTaintsItsParent covers a parent that would take it down.
func TestRefusedChildTaintsItsParent(t *testing.T) {
	snapshot := world()
	child := developer(601, appPID)
	child.Tracer = domain.Yes
	add(&snapshot, child)
	snapshot.Sockets = []domain.Socket{listener(web, "77", 601), listener(api, "78", appPID)}
	plan := ask(snapshot, web, api)
	if plan.Results[0].Refusal.Code != domain.CodeDebugged || plan.Results[1].Refusal.Code != domain.CodeRefusedElsewhere {
		t.Errorf("got %+v", plan.Results)
	}
}

// TestOwnerNamesAreShown prefers a name and falls back to the ID.
func TestOwnerNamesAreShown(t *testing.T) {
	for name, want := range map[string]string{"root": "root", "": "0"} {
		snapshot := world()
		snapshot.Sockets[0].Holders, snapshot.Sockets[0].CreatorUID, snapshot.Sockets[0].CreatorUser = nil, "0", name
		if got := ask(snapshot, web).Results[0].Refusal.Advice.Owner; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// TestUDPIsSeparate proves TCP and UDP never affect each other.
func TestUDPIsSeparate(t *testing.T) {
	snapshot := world()
	udp := domain.Endpoint{Port: 8000, Protocol: domain.UDP}
	add(&snapshot, developer(700, shellPID))
	snapshot.Sockets = append(snapshot.Sockets, listener(udp, "78", 700))
	if target := ask(snapshot, web).Targets[0]; target.Root.PID != appPID {
		t.Errorf("tcp: %+v", target)
	}
	if target := ask(snapshot, udp).Targets[0]; target.Root.PID != 700 {
		t.Errorf("udp: %+v", target)
	}
	snapshot.Sockets = snapshot.Sockets[1:]
	if refusal := ask(snapshot, web).Results[0].Refusal; refusal.Code != domain.CodeNotListening || !refusal.Advice.UDPOnly {
		t.Errorf("got %+v", refusal)
	}
	connected := listener(udp, "79", appPID)
	connected.Connected = true
	snapshot.Sockets = append(snapshot.Sockets, connected)
	if got := codeOf(ask(snapshot, udp)); got != domain.CodeAmbiguous {
		t.Errorf("two UDP sockets, one connected: %q", got)
	}
}

// TestGate proves a closed gate refuses, and comes last.
func TestGate(t *testing.T) {
	closed := gates{graceful: true, families: true}
	request := domain.StopRequest{Endpoints: []domain.Endpoint{web}, Force: true}
	if plan := decide(world(), request, closed); codeOf(plan) != domain.CodePlatform || len(plan.Targets) != 0 || plan.ID != "" {
		t.Errorf("got %+v", plan)
	}
	snapshot := world()
	change(&snapshot, appPID, func(p *domain.Process) { p.Tracer = domain.Yes })
	if got := codeOf(decide(snapshot, request, closed)); got != domain.CodeDebugged {
		t.Errorf("a specific reason should win: %q", got)
	}
	guarded := openGates
	guarded.guard = func(domain.Snapshot, candidate) bool { return false }
	if got := codeOf(decide(world(), request, guarded)); got != domain.CodePlatform {
		t.Errorf("guard ignored: %q", got)
	}
}

// TestContainerGates needs the platform and the engine.
func TestContainerGates(t *testing.T) {
	snapshot := world()
	published(&snapshot)
	request := domain.StopRequest{Endpoints: []domain.Endpoint{web}}
	for name, closed := range map[string]gates{
		"platform": {docker: true, podman: true, graceful: true},
		"engine":   {native: true, podman: true, graceful: true},
	} {
		if got := codeOf(decide(snapshot, request, closed)); got != domain.CodePlatform {
			t.Errorf("%s gate closed: %q", name, got)
		}
	}
}

// TestNoGracefulMethod is the native Windows rule.
func TestNoGracefulMethod(t *testing.T) {
	windows := gates{native: true}
	request := domain.StopRequest{Endpoints: []domain.Endpoint{web}}
	if got := codeOf(decide(world(), request, windows)); got != domain.CodeNoGraceful {
		t.Errorf("plain stop: %q", got)
	}
	request.Force = true
	if plan := decide(world(), request, windows); len(plan.Targets) != 1 || plan.Targets[0].GracefulAvailable {
		t.Errorf("forced: %+v", plan)
	}
}

// TestPlanID changes with anything confirmed and ignores order and noise.
func TestPlanID(t *testing.T) {
	snapshot := world()
	snapshot.Sockets = append(snapshot.Sockets, listener(api, "78", appPID))
	base := ask(snapshot, web).ID
	if len(base) != 32 || ask(snapshot, web).ID != base {
		t.Fatalf("unstable id %q", base)
	}
	noise := world()
	noise.Sockets = append(noise.Sockets, listener(api, "78", appPID), listener(domain.Endpoint{Port: 40000, Protocol: domain.UDP}, "90", appPID))
	change(&noise, appPID, func(p *domain.Process) { p.Name, p.Dir = "renamed", "/elsewhere" })
	add(&noise, developer(601, appPID))
	if ask(noise, web).ID != base {
		t.Error("ephemeral UDP, names or children changed the id")
	}
	if ask(snapshot, web, api).ID != ask(snapshot, api, web).ID || ask(snapshot, web, api).ID == base {
		t.Error("order must not matter, named ports must")
	}
	for name, edit := range map[string]func(*domain.Snapshot){
		"restart":   func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Start = "9" }) },
		"exec":      func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Exe = "/x" }) },
		"socket":    func(s *domain.Snapshot) { s.Sockets[0].ID.OSID = "99" },
		"endpoint":  func(s *domain.Snapshot) { s.Sockets[1].ID.Port = 9009 },
		"suspended": func(s *domain.Snapshot) { change(s, appPID, func(p *domain.Process) { p.Suspended = true }) },
	} {
		changed := world()
		changed.Sockets = append(changed.Sockets, listener(api, "78", appPID))
		edit(&changed)
		if ask(changed, web).ID == base {
			t.Errorf("%s did not change the id", name)
		}
	}
}

// TestDecideIsPure proves the same input gives the same plan and is not modified.
func TestDecideIsPure(t *testing.T) {
	snapshot := family()
	first, second := ask(snapshot, web, api), ask(snapshot, web, api)
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(snapshot, family()) {
		t.Error("decide changed its input or its answer")
	}
}

// TestRandomSnapshotsNeverSelectProtected builds odd worlds and checks every target.
func TestRandomSnapshotsNeverSelectProtected(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		snapshot := randomWorld(random)
		plan := decide(snapshot, domain.StopRequest{Endpoints: []domain.Endpoint{web, api}, Force: random.IntN(2) == 0}, openGates)
		for _, target := range plan.Targets {
			checkTarget(t, snapshot, target)
		}
	}
}

// randomWorld connects random processes, flags and sockets.
func randomWorld(random *rand.Rand) domain.Snapshot {
	snapshot := world()
	pids := []int{1, shellPID, 500, appPID}
	for i := range random.IntN(6) {
		process := developer(700+i, pids[random.IntN(len(pids))])
		process.Kernel, process.UserManager = random.IntN(8) == 0, random.IntN(8) == 0
		process.Tracer = domain.Tri(random.IntN(3))
		process.Service = domain.Tri(1 + random.IntN(2))
		if random.IntN(5) == 0 {
			process.UserID = "0"
		}
		if random.IntN(6) == 0 {
			process.Name = "docker-proxy"
		}
		add(&snapshot, process)
		pids = append(pids, process.PID)
	}
	snapshot.Sockets = nil
	for i := range 1 + random.IntN(4) {
		endpoint := []domain.Endpoint{web, api}[random.IntN(2)]
		holders := []int{pids[random.IntN(len(pids))]}
		if random.IntN(3) == 0 {
			holders = append(holders, pids[random.IntN(len(pids))])
		}
		snapshot.Sockets = append(snapshot.Sockets, listener(endpoint, string(rune('a'+i)), holders...))
	}
	return snapshot
}

// checkTarget fails when a target breaks a structural rule.
func checkTarget(t *testing.T, snapshot domain.Snapshot, target domain.StopTarget) {
	t.Helper()
	own, _ := snapshot.Ancestors(snapshot.Host.SelfPID)
	for _, member := range target.Members {
		process := snapshot.Processes[member.PID]
		isOwn := slices.ContainsFunc(own, func(p domain.Process) bool { return p.PID == member.PID })
		isWrong := member.PID <= 1 || isOwn || process.Kernel || process.UserManager || process.Tracer != domain.No ||
			process.Service != domain.No || process.UserID != "1000" || process.Name == "docker-proxy" || member.PID == 500
		if isWrong {
			t.Fatalf("selected %+v", process)
		}
	}
	for _, socket := range target.Named {
		for _, other := range snapshot.On(socket.Endpoint) {
			for _, holder := range other.Holders {
				if !slices.ContainsFunc(target.Members, func(m domain.ProcessID) bool { return m.PID == holder.PID }) {
					t.Fatalf("port %v has a holder outside the target", socket.Endpoint)
				}
			}
		}
	}
}

// TestSystemAccounts treats unknown users as system accounts.
func TestSystemAccounts(t *testing.T) {
	host := domain.Host{SystemUIDMax: 999}
	for userID, want := range map[string]bool{
		"0": true, "999": true, "1000": false, "65534": true, "": true, "x": true,
		"S-1-5-18": true, "S-1-5-21-1-2-3-1001": false, "S-1-12-1-5": false,
	} {
		if got := isSystemAccount(host, userID); got != want {
			t.Errorf("%q: %v", userID, got)
		}
	}
}

var anyAddress = netip.MustParseAddr("0.0.0.0")
