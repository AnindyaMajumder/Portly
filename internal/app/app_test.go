package app

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
	"github.com/AnindyaMajumder/Portly/internal/modules/stopper"
)

// fakeScanner returns one snapshot.
type fakeScanner struct {
	snapshot domain.Snapshot
	err      error
	rereads  int
}

// Scan returns the snapshot.
func (f *fakeScanner) Scan(context.Context) (domain.Snapshot, error) { return f.snapshot, f.err }

// Observe returns the snapshot.
func (f *fakeScanner) Observe(context.Context, []domain.Endpoint) (domain.Snapshot, error) {
	return f.snapshot, f.err
}

// Reread counts calls.
func (f *fakeScanner) Reread(domain.Holder, domain.SocketID) (domain.Process, bool, error) {
	f.rereads++
	return domain.Process{}, true, nil
}

// fakeEngines returns engines after a delay, or when the deadline ends.
type fakeEngines struct {
	engines []domain.Engine
	delay   time.Duration
}

// Engines waits, and reports a silent engine on timeout.
func (f fakeEngines) Engines(ctx context.Context) ([]domain.Engine, []string) {
	select {
	case <-time.After(f.delay):
		return f.engines, []string{"note"}
	case <-ctx.Done():
		return []domain.Engine{{ID: "slow", Answer: domain.AnswerTimeout}}, nil
	}
}

// fakeProjects names projects after the process.
type fakeProjects struct{ calls *int }

// Detect returns a project for every process.
func (f fakeProjects) Detect(_ context.Context, process domain.Process, _ []domain.Process) *domain.Project {
	if f.calls != nil {
		*f.calls++
	}
	return &domain.Project{Name: "project-of-" + process.Name}
}

// fakeStopper returns a fixed plan and records runs.
type fakeStopper struct {
	plan domain.StopPlan
	ran  []int
	hook func()
}

// Decide returns the plan.
func (f *fakeStopper) Decide(domain.Snapshot, domain.StopRequest) domain.StopPlan { return f.plan }

// Run records the target.
func (f *fakeStopper) Run(_ context.Context, _ domain.StopRequest, target domain.StopTarget, _ stopper.Confirm) domain.Outcome {
	f.ran = append(f.ran, target.Root.PID)
	if f.hook != nil {
		f.hook()
	}
	return domain.Outcome{Delivered: true}
}

// socketOn builds a socket on an address.
func socketOn(address string, port uint16, protocol domain.Protocol, uid string, pids ...int) domain.Socket {
	socket := domain.Socket{CreatorUID: uid}
	socket.ID.Endpoint = domain.Endpoint{Port: port, Protocol: protocol}
	socket.ID.Addr = netip.MustParseAddr(address)
	for _, pid := range pids {
		socket.Holders = append(socket.Holders, domain.Holder{PID: pid})
	}
	return socket
}

// processes builds a process table from pid to parent.
func processes(parents map[int]int) map[int]domain.Process {
	table := map[int]domain.Process{}
	for pid, parent := range parents {
		process := domain.Process{PPID: parent, Name: "app", User: "dev"}
		process.PID, process.UserID, process.Exe = pid, "1000", "/usr/bin/node"
		table[pid] = process
	}
	return table
}

// build wires an app around fakes.
func build(snapshot domain.Snapshot, engines ...domain.Engine) *App {
	world := NewWorld(&fakeScanner{snapshot: snapshot}, fakeEngines{engines: engines})
	return New(world, fakeProjects{}, &fakeStopper{})
}

// host is an unprivileged caller with an ephemeral range.
var host = domain.Host{CallerUID: "1000", Ephemeral: domain.Range{First: 32768, Last: 60999}}

// TestListHidesNoise covers connected and ephemeral UDP, and --all.
func TestListHidesNoise(t *testing.T) {
	connected := socketOn("10.0.0.1", 53, domain.UDP, "1000", 10)
	connected.Connected = true
	app := build(domain.Snapshot{Host: host, Processes: processes(map[int]int{10: 0}), Sockets: []domain.Socket{
		socketOn("127.0.0.1", 8000, domain.TCP, "1000", 10), socketOn("0.0.0.0", 40000, domain.UDP, "1000", 10),
		socketOn("0.0.0.0", 5353, domain.UDP, "1000", 10), connected,
	}})
	ports := func(all bool) ([]uint16, int) {
		report, err := app.List(t.Context(), all)
		if err != nil {
			t.Fatal(err)
		}
		var found []uint16
		for _, port := range report.Ports {
			found = append(found, port.Ref.Port)
		}
		return found, report.HiddenUDP
	}
	if got, hidden := ports(false); !slices.Equal(got, []uint16{5353, 8000}) || hidden != 1 {
		t.Errorf("list: %v, hidden %d", got, hidden)
	}
	if got, hidden := ports(true); !slices.Equal(got, []uint16{5353, 8000, 40000}) || hidden != 0 {
		t.Errorf("all: %v, hidden %d", got, hidden)
	}
}

// TestInspect covers both protocols, free ports, narrowing and hidden UDP.
func TestInspect(t *testing.T) {
	app := build(domain.Snapshot{Host: host, Processes: processes(map[int]int{10: 0}), Sockets: []domain.Socket{
		socketOn("127.0.0.1", 53, domain.TCP, "1000", 10), socketOn("127.0.0.1", 53, domain.UDP, "1000", 10),
		socketOn("0.0.0.0", 40000, domain.UDP, "1000", 10),
	}})
	cases := map[string][]string{
		"53": {"53/tcp listening", "53/udp listening"}, "53/udp": {"53/udp listening"}, "9": {"9 not_listening"}, "40000": {"40000/udp listening"},
	}
	for text, want := range cases {
		ref, _ := domain.ParsePortRef(text)
		report, err := app.Inspect(t.Context(), []domain.PortRef{ref, ref})
		var got []string
		for _, port := range report.Ports {
			got = append(got, port.Ref.String()+" "+string(port.Status))
		}
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: %v %v", text, got, err)
		}
	}
}

// TestOwners covers families, strangers, hidden owners and twins.
func TestOwners(t *testing.T) {
	table := processes(map[int]int{10: 0, 11: 10, 12: 11, 20: 0})
	cases := []struct {
		name    string
		sockets []domain.Socket
		want    []string
		status  domain.Status
	}{
		{"family", []domain.Socket{socketOn("127.0.0.1", 80, domain.TCP, "1000", 10, 11, 12)}, []string{"10 ok 2"}, domain.Listening},
		{"twins", []domain.Socket{socketOn("127.0.0.1", 80, domain.TCP, "1000", 10), socketOn("::1", 80, domain.TCP, "1000", 10)}, []string{"10 ok 0"}, domain.Listening},
		{"strangers", []domain.Socket{socketOn("127.0.0.1", 80, domain.TCP, "1000", 10, 20)}, []string{"10 ok 0", "20 ok 0"}, domain.Listening},
		{"root", []domain.Socket{socketOn("127.0.0.1", 80, domain.TCP, "0"), socketOn("::1", 80, domain.TCP, "0")}, []string{"0 permission_denied 0"}, domain.Inaccessible},
		{"own but hidden", []domain.Socket{socketOn("127.0.0.1", 80, domain.TCP, "1000")}, []string{"0 unknown 0"}, domain.Inaccessible},
	}
	for _, c := range cases {
		report := describe(domain.Snapshot{Host: host, Processes: table, Sockets: c.sockets}, domain.Endpoint{Port: 80, Protocol: domain.TCP})
		var got []string
		for _, owner := range report.Owners {
			pid := 0
			if owner.Process != nil {
				pid = owner.Process.PID
			}
			got = append(got, strings.Join([]string{strconv.Itoa(pid), string(owner.Access), strconv.Itoa(len(owner.Workers))}, " "))
		}
		if !slices.Equal(got, c.want) || report.Status != c.status || report.Ambiguous != (len(c.want) > 1) {
			t.Errorf("%s: %v %s", c.name, got, report.Status)
		}
	}
}

// TestContainersInReports covers published, socketless and host-network containers.
func TestContainersInReports(t *testing.T) {
	address := netip.MustParseAddr("127.0.0.1")
	container := domain.Container{
		ID: "db", Name: "db-1", InitPID: 30, ComposeProject: "shop", Directory: "/code/shop", Running: true,
		Ports: []domain.Binding{{HostAddr: address, HostPort: 5432, Protocol: domain.TCP}, {HostAddr: address, HostPort: 6000, Protocol: domain.TCP}},
	}
	table := processes(map[int]int{10: 0, 30: 0})
	init := table[30]
	init.ContainerIDs = []string{"db"}
	table[30] = init
	snapshot := domain.Snapshot{Host: host, Processes: table, Sockets: []domain.Socket{
		socketOn("127.0.0.1", 5432, domain.TCP, "0"), socketOn("0.0.0.0", 7000, domain.TCP, "0", 30), socketOn("192.168.0.5", 5432, domain.TCP, "1000", 10),
	}}
	app := build(snapshot, domain.Engine{ID: "e", Kind: "podman", Answer: domain.AnswerOK, Containers: []domain.Container{container}})
	report, err := app.List(t.Context(), false)
	if err != nil || len(report.Ports) != 3 || len(report.Notes) != 1 {
		t.Fatalf("got %+v %v", report, err)
	}
	published, socketless, hostNet := report.Ports[0], report.Ports[1], report.Ports[2]
	if len(published.Owners) != 2 || !published.Ambiguous || published.Owners[0].Type != "Podman" || !published.Owners[0].Proven {
		t.Errorf("published: %+v", published)
	}
	if project := published.Owners[0].Project; project == nil || project.Name != "shop" || project.Root != "/code/shop" {
		t.Errorf("project: %+v", project)
	}
	if socketless.Status != domain.Published || hostNet.Owners[0].Container == nil || hostNet.Status != domain.Listening {
		t.Errorf("socketless %+v, host network %+v", socketless, hostNet)
	}
}

// TestProjectsAreDetectedOnce covers one process on several ports.
func TestProjectsAreDetectedOnce(t *testing.T) {
	calls := 0
	snapshot := domain.Snapshot{Host: host, Processes: processes(map[int]int{10: 0}), Sockets: []domain.Socket{
		socketOn("127.0.0.1", 80, domain.TCP, "1000", 10), socketOn("127.0.0.1", 81, domain.TCP, "1000", 10),
	}}
	app := New(NewWorld(&fakeScanner{snapshot: snapshot}, fakeEngines{}), fakeProjects{&calls}, nil)
	report, _ := app.List(t.Context(), false)
	if calls != 1 || report.Ports[1].Owners[0].Project.Name != "project-of-app" {
		t.Errorf("%d calls, %+v", calls, report.Ports[1].Owners[0].Project)
	}
}

// TestSlowEnginesDoNotBlock lists natively and records the silence.
func TestSlowEnginesDoNotBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		world := NewWorld(&fakeScanner{snapshot: domain.Snapshot{Host: host}}, fakeEngines{delay: time.Hour})
		started := time.Now()
		snapshot, _, err := world.list(t.Context())
		if err != nil || time.Since(started) != listEngineDeadline || snapshot.Engines[0].Answer != domain.AnswerTimeout {
			t.Errorf("%v after %v, %+v", err, time.Since(started), snapshot.Engines)
		}
		started = time.Now()
		if _, err := world.Observe(t.Context(), nil); err != nil || time.Since(started) != stopEngineDeadline {
			t.Errorf("%v after %v", err, time.Since(started))
		}
	})
}

// TestWorldPassesThrough covers errors, looks and rereads.
func TestWorldPassesThrough(t *testing.T) {
	scanner := &fakeScanner{err: errors.New("broken")}
	world := NewWorld(scanner, fakeEngines{})
	if _, err := world.Observe(t.Context(), nil); err == nil {
		t.Error("observe hid the error")
	}
	if _, err := world.Look(t.Context()); err == nil {
		t.Error("look hid the error")
	}
	if _, holds, _ := world.Reread(domain.Holder{}, domain.SocketID{}); !holds || scanner.rereads != 1 {
		t.Error("reread did not pass through")
	}
	app := New(world, fakeProjects{}, &fakeStopper{})
	if _, err := app.List(t.Context(), false); err == nil {
		t.Error("list hid the error")
	}
	if _, err := app.Inspect(t.Context(), nil); err == nil {
		t.Error("inspect hid the error")
	}
	if _, err := app.Plan(t.Context(), domain.StopRequest{}, ""); err == nil {
		t.Error("plan hid the error")
	}
}

// TestPlanAndExpect covers names, matching ids and changed targets.
func TestPlanAndExpect(t *testing.T) {
	snapshot := domain.Snapshot{Host: host, Processes: processes(map[int]int{10: 0, 11: 10})}
	fake := &fakeStopper{plan: domain.StopPlan{ID: "abc", Targets: []domain.StopTarget{
		{Kind: domain.KindProcess, Root: domain.ProcessID{PID: 10}, Children: []domain.ProcessID{{PID: 11}}},
		{Kind: domain.KindContainer, Container: domain.ContainerRef{Engine: "e", ID: "db"}},
	}}}
	engine := domain.Engine{ID: "e", Containers: []domain.Container{{ID: "db", Name: "db-1", ComposeProject: "shop"}}}
	app := New(NewWorld(&fakeScanner{snapshot: snapshot}, fakeEngines{engines: []domain.Engine{engine}}), fakeProjects{}, fake)
	plan, err := app.Plan(t.Context(), domain.StopRequest{}, "abc")
	if err != nil || len(plan.Targets) != 2 {
		t.Fatalf("%+v %v", plan, err)
	}
	if plan.Targets[0].Process.PID != 10 || plan.Targets[0].Project == nil || plan.Targets[0].Children[0].PID != 11 {
		t.Errorf("process: %+v", plan.Targets[0])
	}
	if plan.Targets[1].Container.Name != "db-1" || plan.Targets[1].Project.Name != "shop" {
		t.Errorf("container: %+v", plan.Targets[1])
	}
	if _, err := app.Plan(t.Context(), domain.StopRequest{}, "other"); err == nil || domain.AsError(err).Code != domain.CodeChanged {
		t.Errorf("expect: %v", err)
	}
	if len(fake.ran) != 0 {
		t.Error("planning must not act")
	}
}

// TestExecute runs targets in order and stops when cancelled.
func TestExecute(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	fake := &fakeStopper{}
	plan := Plan{Stop: domain.StopPlan{Targets: []domain.StopTarget{{Root: domain.ProcessID{PID: 1}}, {Root: domain.ProcessID{PID: 2}}}}}
	app := New(nil, nil, fake)
	if outcomes := app.Execute(ctx, plan, nil); len(outcomes) != 2 || !slices.Equal(fake.ran, []int{1, 2}) {
		t.Errorf("%+v %v", outcomes, fake.ran)
	}
	fake.ran, fake.hook = nil, cancel
	if outcomes := app.Execute(ctx, plan, nil); outcomes[1].Code != domain.CodeCancelled || !slices.Equal(fake.ran, []int{1}) {
		t.Errorf("%+v %v", outcomes, fake.ran)
	}
}

// TestKinds covers runtimes and unknown programs.
func TestKinds(t *testing.T) {
	for exe, want := range map[string]string{
		"/usr/bin/node": "Node", "/usr/bin/python3.12": "Python", `C:\Program Files\nodejs\node.exe`: "Node", "/usr/lib/jvm/bin/java": "Java", "/bin/x": "Native", "": "Python",
	} {
		process := domain.Process{Name: "uvicorn"}
		process.Exe = exe
		if got := kindOf(process); got != want {
			t.Errorf("%s: %s", exe, got)
		}
	}
	if UserName("0") != "root" && UserName("0") != "0" || UserName("987654") != "987654" || UserName("") != "" {
		t.Error("user names")
	}
}
