package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/app"
	"github.com/AnindyaMajumder/Portly/internal/domain"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fakeApp returns fixed results and records what was asked.
type fakeApp struct {
	report    app.Report
	plan      app.Plan
	outcomes  []domain.Outcome
	err       error
	refs      []domain.PortRef
	all       bool
	request   domain.StopRequest
	expect    string
	executed  int
	asksForce bool
	forced    bool
	panics    bool
	closed    bool
	// For portly update
	updatePlan app.UpdatePlan
	updateErr  error
}

// List returns the report.
func (f *fakeApp) List(_ context.Context, all bool) (app.Report, error) {
	if f.panics {
		panic("boom")
	}
	f.all = all
	return f.report, f.err
}

// Inspect returns the report.
func (f *fakeApp) Inspect(_ context.Context, refs []domain.PortRef) (app.Report, error) {
	f.refs = refs
	return f.report, f.err
}

// Plan returns the plan.
func (f *fakeApp) Plan(_ context.Context, request domain.StopRequest, expect string) (app.Plan, error) {
	f.request, f.expect = request, expect
	return f.plan, f.err
}

// CheckUpdate returns the fixed update plan.
func (f *fakeApp) CheckUpdate(_ context.Context, current string) (app.UpdatePlan, error) {
	f.updatePlan.Current = current
	return f.updatePlan, f.err
}

// Update records the install, or fails like the app.
func (f *fakeApp) Update(context.Context, app.UpdatePlan) error {
	f.executed++
	return f.updateErr
}

// CanStop is true unless a test closes the gate.
func (f *fakeApp) CanStop() bool { return !f.closed }

// Execute records the run, and may ask for a force kill.
func (f *fakeApp) Execute(ctx context.Context, plan app.Plan, confirm app.Confirm) []domain.Outcome {
	f.executed++
	if f.asksForce {
		f.forced = confirm(ctx, plan.Stop.Targets[0])
	}
	return f.outcomes
}

// run is one command line and what it printed.
type run struct {
	exit     int
	out, err string
}

// invoke runs a command line against a fake app.
func invoke(t *testing.T, useCases UseCases, input string, edit func(*Console), args ...string) run {
	t.Helper()
	var out, errOut bytes.Buffer
	console := Console{In: strings.NewReader(input), Out: &out, Err: &errOut, Version: "v1", Executable: "/usr/local/bin/portly", Home: "/home/dev"}
	if edit != nil {
		edit(&console)
	}
	exit := Run(t.Context(), args, console, useCases)
	return run{exit, out.String(), errOut.String()}
}

// interactive gives the console a terminal.
func interactive(console *Console) { console.Interactive, console.Width = true, 120 }

// socket builds a listening socket.
func socket(address string, port uint16, protocol domain.Protocol) domain.Socket {
	found := domain.Socket{Evidence: []domain.Evidence{{Source: "proc-net", Completeness: domain.Complete}}}
	found.ID.Endpoint = domain.Endpoint{Port: port, Protocol: protocol}
	found.ID.Addr, found.ID.Family = netip.MustParseAddr(address), 4
	if found.ID.Addr.Is6() {
		found.ID.Family = 6
	}
	return found
}

// webProcess is a typical development server with a secret argument.
func webProcess() *domain.Process {
	process := &domain.Process{Name: "uvicorn", User: "dev", Dir: "/home/dev/shop", Args: []string{"uvicorn", "main:app", "--token", "abc123"}}
	process.PID, process.Exe = 6182, "/usr/bin/python3"
	return process
}

// shopProject is the project of the development server.
func shopProject() *domain.Project {
	return &domain.Project{Name: "shop", Root: "/home/dev/shop", Confidence: domain.High, Evidence: "pyproject.toml"}
}

// sampleReport covers every kind of owner.
func sampleReport() app.Report {
	container := &domain.Container{ID: strings.Repeat("d", 64), Name: "shop-db-1", Image: "postgres:18", Engine: "/run/docker.sock", ComposeProject: "shop", ComposeService: "db", Directory: "/home/dev/shop"}
	port := func(number uint16, protocol domain.Protocol, status domain.Status, owners ...domain.Owner) domain.PortReport {
		return domain.PortReport{Ref: domain.PortRef{Port: number, Protocol: protocol}, Status: status, Ambiguous: len(owners) > 1, Owners: owners}
	}
	web := domain.Owner{Type: "Python", Access: domain.AccessOK, User: "dev", Process: webProcess(), Project: shopProject(), Workers: []int{6190, 6191},
		Listeners: []domain.Socket{socket("127.0.0.1", 8000, domain.TCP), socket("::1", 8000, domain.TCP)}}
	other := webProcess()
	other.PID, other.Name, other.Dir, other.Args = 7000, "other", "", nil
	return app.Report{HiddenUDP: 3, Ports: []domain.PortReport{
		port(22, domain.TCP, domain.Inaccessible, domain.Owner{Type: "Native", Access: domain.AccessDenied, User: "root", Listeners: []domain.Socket{socket("0.0.0.0", 22, domain.TCP)}}),
		port(5353, domain.UDP, domain.Listening, domain.Owner{Type: "Native", Access: domain.AccessUnknown, Listeners: []domain.Socket{socket("0.0.0.0", 5353, domain.UDP)}}),
		port(5432, domain.TCP, domain.Listening, domain.Owner{Type: "Docker", Access: domain.AccessOK, Container: container, Proven: true,
			Project: &domain.Project{Name: "shop", Root: "/home/dev/shop", Confidence: domain.High, Evidence: "compose label"}, Listeners: []domain.Socket{socket("127.0.0.1", 5432, domain.TCP)}}),
		port(6000, domain.TCP, domain.Published, domain.Owner{Type: "Podman", Access: domain.AccessOK, Container: &domain.Container{ID: strings.Repeat("e", 64), Name: "cache", Image: "sha256:" + strings.Repeat("f", 64), Engine: "/run/podman.sock"}}),
		port(8000, domain.TCP, domain.Listening, web),
		port(9000, domain.TCP, domain.Listening, web, domain.Owner{Type: "Native", Access: domain.AccessOK, Process: other, Listeners: []domain.Socket{socket("192.168.1.2", 9000, domain.TCP)}}),
		port(9999, "", domain.NotListening),
	}}
}

// golden compares output with a checked-in file.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("%s differs, got:\n%s", name, got)
	}
}

// TestGoldenOutput pins every text layout.
func TestGoldenOutput(t *testing.T) {
	width := func(columns int) func(*Console) { return func(c *Console) { c.Width = columns } }
	listOnly := sampleReport()
	listOnly.Ports = listOnly.Ports[:6]
	cases := []struct {
		name string
		edit func(*Console)
		args []string
	}{
		{"list-pipe", nil, nil}, {"list-wide", width(140), nil}, {"list-80", width(80), nil}, {"list-40", width(40), nil},
		{"list-color", func(c *Console) { c.Width, c.Color = 80, true }, nil},
		{"details", nil, []string{"22", "5353", "5432", "6000", "8000", "9000", "9999"}},
		{"help", nil, []string{"--help"}}, {"help-stop", nil, []string{"stop", "--help"}}, {"help-update", nil, []string{"update", "--help"}}, {"version", nil, []string{"--version"}},
	}
	for _, c := range cases {
		report := listOnly
		if len(c.args) > 1 {
			report = sampleReport()
		}
		got := invoke(t, &fakeApp{report: report}, "", c.edit, c.args...)
		if got.exit != 0 || got.err != "" {
			t.Errorf("%s: exit %d, %s", c.name, got.exit, got.err)
		}
		golden(t, c.name, got.out)
	}
}

// TestNarrowListFits never wraps unless even the minimum is too wide.
func TestNarrowListFits(t *testing.T) {
	for _, columns := range []int{60, 80, 100, 160} {
		got := invoke(t, &fakeApp{report: sampleReport()}, "", func(c *Console) { c.Width = columns })
		for line := range strings.Lines(got.out) {
			if len([]rune(strings.TrimRight(line, "\n"))) > columns && !strings.Contains(line, "·") {
				t.Errorf("%d columns: %q", columns, line)
			}
		}
	}
}

// TestJSONMatchesTheSchema covers every command and error.
func TestJSONMatchesTheSchema(t *testing.T) {
	allowed := samplePlan()
	cases := []struct {
		name string
		app  *fakeApp
		args []string
		exit int
	}{
		{"list", &fakeApp{report: sampleReport()}, []string{"--json"}, 0},
		{"empty list", &fakeApp{}, []string{"--json"}, 0},
		{"inspect", &fakeApp{report: sampleReport()}, []string{"8000", "--json"}, 0},
		{"bad port", &fakeApp{}, []string{"x", "--json"}, 2},
		{"bad flag", &fakeApp{}, []string{"--json", "--nope"}, 2},
		{"bad stop flag", &fakeApp{}, []string{"stop", "--nope", "--json"}, 2},
		{"no port", &fakeApp{}, []string{"stop", "--json"}, 2},
		{"scan failed", &fakeApp{err: errors.New("broken")}, []string{"--json"}, 1},
		{"panic", &fakeApp{panics: true}, []string{"--json"}, 1},
		{"dry run", &fakeApp{plan: allowed}, []string{"stop", "8000", "5432", "22", "--json"}, 6},
		{"stopped", &fakeApp{plan: allowed, outcomes: sampleOutcomes()}, []string{"stop", "8000", "5432", "22", "--json", "--yes"}, 8},
		{"all refused", &fakeApp{plan: refusedPlan()}, []string{"stop", "22", "--json"}, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := invoke(t, c.app, "", nil, c.args...)
			if got.exit != c.exit {
				t.Errorf("exit %d, want %d", got.exit, c.exit)
			}
			document := checkDocument(t, got.out)
			if c.exit != 0 && c.exit != 4 && c.exit != 8 && document["error"] == nil {
				t.Error("no error in the document")
			}
			if strings.Contains(got.out, "abc123") || strings.Contains(got.out, "\x1b") {
				t.Error("secret or colour in JSON")
			}
		})
	}
}

// samplePlan has a process, a container and a refusal.
func samplePlan() app.Plan {
	root := domain.ProcessID{PID: 6182, Start: "1", UserID: "1000", Exe: "/usr/bin/python3"}
	web := domain.StopTarget{
		Kind: domain.KindFamily, Root: root, Members: []domain.ProcessID{root, {PID: 6190}}, GracefulAvailable: true, Suspended: true, Reloader: true,
		Named: []domain.SocketID{socket("127.0.0.1", 8000, domain.TCP).ID}, Other: []domain.AddressPort{socket("::1", 9000, domain.TCP).ID.AddressPort},
	}
	db := domain.StopTarget{
		Kind: domain.KindContainer, Container: domain.ContainerRef{Engine: "e", ID: "db", StartedAt: "t"}, GracefulAvailable: true, Paused: true, AutoRemove: true,
		SharesNet: []string{"sidecar"}, Bindings: []domain.Binding{{HostPort: 5432, Protocol: domain.TCP}},
	}
	endpoint := func(port uint16) domain.Endpoint { return domain.Endpoint{Port: port, Protocol: domain.TCP} }
	return app.Plan{
		Stop: domain.StopPlan{ID: strings.Repeat("a", 32), Targets: []domain.StopTarget{web, db}, Results: []domain.PortResult{
			{Endpoint: endpoint(8000), Target: 0}, {Endpoint: endpoint(5432), Target: 1},
			{Endpoint: endpoint(22), Target: -1, Refusal: domain.Refusal{Code: domain.CodePermissionDenied, Advice: domain.Advice{NeedsAdmin: true, Owner: "root"}}},
		}},
		Targets: []app.Target{
			{Stop: web, Process: webProcess(), Project: shopProject(), Children: []domain.Process{*webProcess()}},
			{Stop: db, Container: &domain.Container{ID: "db", Name: "shop-db-1", ComposeProject: "shop", ComposeService: "db", Directory: "/home/dev/shop"}, Proven: true},
		},
	}
}

// onePlan keeps only the process of the sample plan.
func onePlan() app.Plan {
	plan := samplePlan()
	plan.Stop.Targets, plan.Targets, plan.Stop.Results = plan.Stop.Targets[:1], plan.Targets[:1], plan.Stop.Results[:1]
	return plan
}

// refusedPlan has nothing to stop.
func refusedPlan() app.Plan {
	plan := samplePlan()
	return app.Plan{Stop: domain.StopPlan{Results: plan.Stop.Results[2:]}}
}

// sampleOutcomes stops the process and leaves the container pending.
func sampleOutcomes() []domain.Outcome {
	return []domain.Outcome{
		{Method: domain.Graceful, ProcessExited: domain.Yes, PortReleased: domain.Yes, Delivered: true},
		{Code: domain.CodePending, Method: domain.Graceful, ProcessExited: domain.No, Delivered: true},
	}
}

// TestStopConversations pins prompts, results and exit statuses.
func TestStopConversations(t *testing.T) {
	stopped := []domain.Outcome{{Method: domain.Graceful, Delivered: true}}
	cases := []struct {
		name  string
		app   *fakeApp
		input string
		args  []string
		exit  int
		runs  int
	}{
		{"stop-yes", &fakeApp{plan: onePlan(), outcomes: stopped}, "y\n", []string{"stop", "8000"}, 0, 1},
		{"stop-no", &fakeApp{plan: onePlan()}, "n\n", []string{"stop", "8000"}, 6, 0},
		{"stop-empty", &fakeApp{plan: onePlan()}, "\n", []string{"stop", "8000"}, 6, 0},
		{"stop-eof", &fakeApp{plan: onePlan()}, "", []string{"stop", "8000"}, 6, 0},
		{"stop-several", &fakeApp{plan: samplePlan(), outcomes: sampleOutcomes()}, "YES\n", []string{"stop", "8000", "5432", "22"}, 8, 1},
		{"stop-flag", &fakeApp{plan: onePlan(), outcomes: stopped}, "", []string{"--yes", "stop", "8000"}, 0, 1},
		{"stop-refused", &fakeApp{plan: refusedPlan()}, "", []string{"stop", "22"}, 4, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := invoke(t, c.app, c.input, interactive, c.args...)
			if got.exit != c.exit || c.app.executed != c.runs {
				t.Errorf("exit %d, executed %d", got.exit, c.app.executed)
			}
			golden(t, c.name, "stdout:\n"+got.out+"stderr:\n"+got.err)
		})
	}
}

// TestNothingRunsWithoutConfirmation covers pipes and JSON.
func TestNothingRunsWithoutConfirmation(t *testing.T) {
	for name, args := range map[string][]string{
		"pipe": {"stop", "8000"}, "pipe force": {"stop", "8000", "--force"}, "json": {"stop", "8000", "--json"}, "json force": {"stop", "8000", "--json", "--force"},
	} {
		fake := &fakeApp{plan: onePlan()}
		if got := invoke(t, fake, "y\n", nil, args...); got.exit != 6 || fake.executed != 0 {
			t.Errorf("%s: exit %d, executed %d", name, got.exit, fake.executed)
		}
	}
	fake := &fakeApp{plan: onePlan()}
	if got := invoke(t, fake, "y\n", interactive, "stop", "8000", "--json"); got.exit != 6 || fake.executed != 0 || strings.Contains(got.err, "[y/N]") {
		t.Errorf("json on a terminal prompted: %+v", got)
	}
}

// TestForceConfirmation asks a second time unless --yes.
func TestForceConfirmation(t *testing.T) {
	cases := []struct {
		name, input string
		args        []string
		want        bool
		asked       int
	}{
		{"typed kill", "y\nKILL\n", []string{"stop", "8000", "--force"}, true, 1},
		{"impatient y", "y\ny\n", []string{"stop", "8000", "--force"}, false, 1},
		{"impatient yes", "y\nyes\n", []string{"stop", "8000", "--force"}, false, 1},
		{"second no", "y\nn\n", []string{"stop", "8000", "--force"}, false, 1},
		{"second missing", "y\n", []string{"stop", "8000", "--force"}, false, 1},
		{"yes flag", "", []string{"stop", "8000", "--force", "--yes"}, true, 0},
	}
	for _, c := range cases {
		fake := &fakeApp{plan: onePlan(), outcomes: []domain.Outcome{{Delivered: true}}, asksForce: true}
		got := invoke(t, fake, c.input, interactive, c.args...)
		if fake.forced != c.want || strings.Count(got.err, "[y/N]") != c.asked {
			t.Errorf("%s: forced %v, %d prompts", c.name, fake.forced, strings.Count(got.err, "[y/N]"))
		}
	}
	s := &session{options: options{json: true}, console: Console{Interactive: true}}
	if s.confirmForce(onePlan())(t.Context(), domain.StopTarget{}) {
		t.Error("JSON without --yes confirmed a kill")
	}
}

// TestKillQuestionIsExplicit warns when nothing graceful exists.
func TestKillQuestionIsExplicit(t *testing.T) {
	plan := onePlan()
	plan.Targets[0].Stop.GracefulAvailable = false
	got := invoke(t, &fakeApp{plan: plan, outcomes: []domain.Outcome{{Method: domain.Forced}}}, "y\n", interactive, "stop", "8000", "--force")
	if !strings.Contains(got.err, "Kill this process?") || !strings.Contains(got.err, "unsaved data") || !strings.Contains(got.out, "Killed uvicorn") {
		t.Errorf("got %+v", got)
	}
}

// TestCancelDuringPrompt covers Ctrl-C.
func TestCancelDuringPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, errOut bytes.Buffer
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	fake := &fakeApp{plan: onePlan()}
	exit := Run(ctx, []string{"stop", "8000"}, Console{In: reader, Out: &out, Err: &errOut, Interactive: true}, fake)
	if exit != 6 || fake.executed != 0 {
		t.Errorf("exit %d, executed %d", exit, fake.executed)
	}
}

// planID is a well-formed plan id.
var planID = strings.Repeat("a", 32)

// TestExpectNeedsAPlanID proves an empty variable never means any target.
func TestExpectNeedsAPlanID(t *testing.T) {
	for _, args := range [][]string{
		{"stop", "8000", "--yes", "--force", "--expect", ""}, {"stop", "8000", "--yes", "--expect="}, {"stop", "8000", "--yes", "--expect", "zzz"},
		{"stop", "8000", "--yes", "--expect", planID + "0"}, {"--expect", "", "stop", "8000", "--yes"},
	} {
		fake := &fakeApp{plan: onePlan(), outcomes: []domain.Outcome{{}}}
		if got := invoke(t, fake, "", nil, args...); got.exit != 2 || fake.executed != 0 {
			t.Errorf("%q: exit %d, executed %d", args, got.exit, fake.executed)
		}
	}
}

// TestClosedGateIsSaid never suggests a stop that would be refused.
func TestClosedGateIsSaid(t *testing.T) {
	closed := &fakeApp{report: sampleReport(), plan: refusedPlan(), closed: true}
	details := invoke(t, closed, "", nil, "8000")
	if strings.Contains(details.out, "portly stop") || !strings.Contains(details.out, stopDisabled) {
		t.Errorf("details: %s", details.out)
	}
	if got := invoke(t, closed, "", nil, "stop", "22"); strings.Contains(got.err, "sudo") || !strings.Contains(got.err, stopDisabled) {
		t.Errorf("refusal: %s", got.err)
	}
	if got := invoke(t, closed, "", nil, "stop", "--help"); !strings.Contains(got.out, stopDisabled) {
		t.Errorf("help: %s", got.out)
	}
}

// TestCobraErrorsAreUsage covers arguments of built-in commands.
func TestCobraErrorsAreUsage(t *testing.T) {
	for _, args := range [][]string{{"completion", "bash", "extra"}, {"-1"}, {"stop", "-5"}, {"--bogus"}, {"--bogus", "--json=true"}} {
		got := invoke(t, &fakeApp{}, "", nil, args...)
		if got.exit != 2 {
			t.Errorf("%v: %+v", args, got)
		}
		if slices.Contains(args, "--json=true") {
			checkDocument(t, got.out)
		}
	}
}

// TestNotesAreVerbose covers engine notes on stop.
func TestNotesAreVerbose(t *testing.T) {
	plan := refusedPlan()
	plan.Notes = []string{"remote ignored"}
	if got := invoke(t, &fakeApp{plan: plan}, "", nil, "stop", "22", "--verbose"); !strings.Contains(got.err, "Note: remote ignored") {
		t.Errorf("got %s", got.err)
	}
}

// TestCancelledTargetsSaySo covers the rest of an interrupted command.
func TestCancelledTargetsSaySo(t *testing.T) {
	outcomes := []domain.Outcome{{Code: domain.CodeUnknownOutcome, Delivered: true}, {Code: domain.CodeCancelled}}
	got := invoke(t, &fakeApp{plan: samplePlan(), outcomes: outcomes}, "", nil, "stop", "8000", "5432", "22", "--yes", "--json")
	document := checkDocument(t, got.out)
	if status := document["results"].([]any)[1].(map[string]any)["status"]; status != "cancelled" {
		t.Errorf("got %v", status)
	}
}

// TestArguments covers order, protocols, flags and typos.
func TestArguments(t *testing.T) {
	fake := &fakeApp{plan: refusedPlan()}
	invoke(t, fake, "", nil, "--force", "--expect", planID, "stop", "3000", "53/udp", ":80", "--yes")
	want := []domain.Endpoint{{Port: 3000, Protocol: domain.TCP}, {Port: 53, Protocol: domain.UDP}, {Port: 80, Protocol: domain.TCP}}
	if !slices.Equal(fake.request.Endpoints, want) || !fake.request.Force || fake.expect != planID {
		t.Errorf("got %+v %q", fake.request, fake.expect)
	}
	invoke(t, fake, "", nil, "--all")
	if !fake.all {
		t.Error("--all was lost")
	}
	invoke(t, fake, "", nil, ":8000", "53/udp")
	if len(fake.refs) != 2 || fake.refs[1].Protocol != domain.UDP {
		t.Errorf("got %v", fake.refs)
	}
	for _, c := range []struct {
		args []string
		hint string
	}{
		{[]string{"sotp", "8000"}, "Did you mean 'portly stop 8000'?"}, {[]string{"list"}, "Run 'portly' to list"}, {[]string{"version"}, "portly --version"}, {[]string{"kill", "8000"}, "Did you mean 'portly stop 8000'?"}, {[]string{"8000", "x"}, "Use a number"}, {[]string{"8000", "--yes"}, "--help"},
		{[]string{"--all", "8000"}, "portly --all"}, {[]string{"stop", "8000", "x"}, "Use a number"}, {[]string{"stop"}, "portly stop 8000"}, {[]string{"stop", "--all", "1"}, "--help"},
	} {
		planned := &fakeApp{plan: onePlan()}
		got := invoke(t, planned, "", nil, c.args...)
		if got.exit != 2 || !strings.Contains(got.err, c.hint) || got.out != "" || planned.executed != 0 {
			t.Errorf("%v: %+v", c.args, got)
		}
	}
}

// TestEveryCodeIsExplained covers each refusal and result with a next step.
func TestEveryCodeIsExplained(t *testing.T) {
	s := &session{app: &fakeApp{}, console: Console{Executable: "/opt/my apps/portly"}}
	endpoint := domain.Endpoint{Port: 8000, Protocol: domain.TCP}
	for _, code := range domain.Codes() {
		_, isRefusal := reasons[code]
		_, isResult := results[code]
		isCommandLevel := slices.Contains([]domain.Code{domain.CodeUsage, domain.CodeNotListening, domain.CodeConfirmationRequired,
			domain.CodeUpdateUnavailable, domain.CodeChecksumMismatch, domain.CodePackageManaged, domain.CodeDevBuild}, code)
		if !isRefusal && !isResult && !isCommandLevel {
			t.Errorf("%s has no explanation", code)
		}
		failure := s.explainOutcome(endpoint, "app (PID 1)", domain.Outcome{Code: code, Delivered: isResult})
		if failure.Hint == "" || strings.Contains(failure.Message+failure.Hint, "%s") {
			t.Errorf("%s: %+v", code, failure)
		}
	}
	delivered := s.explainOutcome(endpoint, "app", domain.Outcome{Code: domain.CodeChanged, Delivered: true})
	if !strings.Contains(delivered.Message, "was asked to stop") {
		t.Errorf("a delivered stop must be mentioned: %q", delivered.Message)
	}
}

// TestHints covers administrator commands, units, UDP and quoting.
func TestHints(t *testing.T) {
	tcp, udp := domain.Endpoint{Port: 22, Protocol: domain.TCP}, domain.Endpoint{Port: 53, Protocol: domain.UDP}
	admin := domain.Refusal{Code: domain.CodePermissionDenied, Advice: domain.Advice{NeedsAdmin: true, Owner: "root"}}
	unit := func(advice domain.Advice) domain.Refusal {
		return domain.Refusal{Code: domain.CodeSupervised, Advice: advice}
	}
	unix, windows := Console{Executable: "/opt/my apps/portly"}, Console{Windows: true}
	cases := []struct {
		console  Console
		endpoint domain.Endpoint
		refusal  domain.Refusal
		want     string
	}{
		{unix, tcp, admin, "sudo '/opt/my apps/portly' stop 22"}, {unix, udp, admin, "stop 53/udp"}, {windows, tcp, admin, "administrator terminal"},
		{unix, tcp, domain.Refusal{Code: domain.CodeNotListening, Advice: domain.Advice{UDPOnly: true}}, "portly stop 22/udp"},
		{unix, tcp, unit(domain.Advice{Unit: "web.service", CanStopUnit: true}), "sudo systemctl stop web.service"},
		{unix, tcp, unit(domain.Advice{Unit: "web.service", UserUnit: true, CanStopUnit: true}), "systemctl --user stop web.service"},
		{unix, tcp, unit(domain.Advice{Unit: "ssh.service"}), "systemctl status ssh.service"},
		{unix, tcp, unit(domain.Advice{}), "through its supervisor"},
	}
	for _, c := range cases {
		s := &session{app: &fakeApp{}, console: c.console}
		if got := s.explain(c.endpoint, c.refusal); !strings.Contains(got.Hint, c.want) {
			t.Errorf("got %q, want %q", got.Hint, c.want)
		}
	}
	for path, want := range map[string]string{"": "portly", "/a/b": "/a/b", "/a b": "'/a b'", "/a'b": `'/a'\''b'`, "-x": "'-x'", "/a;b": "'/a;b'", "/a$(x)": "'/a$(x)'"} {
		if got := quote(path); got != want {
			t.Errorf("quote(%q) = %s", path, got)
		}
	}
}

// TestHostileNamesAreCleaned covers every output.
func TestHostileNamesAreCleaned(t *testing.T) {
	report := sampleReport()
	evil := "\x1b]0;owned\a\u202e"
	owner := &report.Ports[4].Owners[0]
	owner.Process.Name, owner.Process.Dir, owner.Process.User, owner.Project.Name = evil, "/"+evil, evil, evil
	owner.Process.Args = []string{evil}
	plan := samplePlan()
	plan.Targets[0].Process.Name, plan.Targets[0].Project.Name, plan.Targets[1].Container.Name = evil, evil, evil
	plan.Targets[0].Children[0].Name = evil
	plan.Targets[1].Stop.SharesNet = []string{evil}
	for _, args := range [][]string{{}, {"8000"}, {"--json"}, {"stop", "8000"}, {"stop", "8000", "--json"}, {"stop", "8000", "--yes"}, {evil}, {"kill", evil}} {
		got := invoke(t, &fakeApp{report: report, plan: plan, outcomes: sampleOutcomes()}, "y\n", interactive, args...)
		if strings.ContainsAny(got.out+got.err, "\x1b\a\u202e") {
			t.Errorf("%v leaked control characters", args)
		}
	}
}

// TestDiagnostics go to stderr only.
func TestDiagnostics(t *testing.T) {
	report := app.Report{Warnings: []string{"partial"}, Notes: []string{"remote ignored"}}
	quiet := invoke(t, &fakeApp{report: report}, "", nil)
	verbose := invoke(t, &fakeApp{report: report}, "", nil, "--verbose")
	if quiet.err != "Warning: partial\n" || verbose.err != "Warning: partial\nNote: remote ignored\n" || strings.Contains(verbose.out, "partial") {
		t.Errorf("%q %q", quiet.err, verbose.err)
	}
	if got := invoke(t, &fakeApp{panics: true}, "", nil); got.exit != 1 || strings.Contains(got.err, "goroutine") {
		t.Errorf("panic reached the user: %+v", got)
	}
	if got := invoke(t, &fakeApp{err: context.Canceled}, "", nil); got.exit != 6 {
		t.Errorf("cancelled: %+v", got)
	}
}

// failingWriter breaks like a closed pipe.
type failingWriter struct{}

// Write always fails.
func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

// TestLostOutputIsAnError proves a full disk is never success.
func TestLostOutputIsAnError(t *testing.T) {
	var errOut bytes.Buffer
	console := Console{In: strings.NewReader(""), Out: failingWriter{}, Err: &errOut, Version: "v1"}
	for _, args := range [][]string{{"--json"}, {}, {"--version"}} {
		errOut.Reset()
		if exit := Run(t.Context(), args, console, &fakeApp{report: sampleReport()}); exit != 1 || !strings.Contains(errOut.String(), "write output") {
			t.Errorf("%v: exit %d, %q", args, exit, errOut.String())
		}
	}
}

// TestExitStatus covers equal and mixed results.
func TestExitStatus(t *testing.T) {
	result := func(target int, code domain.Code) domain.PortResult {
		return domain.PortResult{Target: target, Refusal: domain.Refusal{Code: code}}
	}
	cases := []struct {
		results  []domain.PortResult
		outcomes []domain.Outcome
		want     int
	}{
		{[]domain.PortResult{result(0, "")}, []domain.Outcome{{}}, 0},
		{[]domain.PortResult{result(-1, domain.CodeAmbiguous), result(-1, domain.CodeProtected)}, nil, 5},
		{[]domain.PortResult{result(0, ""), result(-1, domain.CodeProtected)}, []domain.Outcome{{}}, 8},
		{[]domain.PortResult{result(0, ""), result(0, "")}, []domain.Outcome{{Code: domain.CodePending}}, 7},
		{[]domain.PortResult{result(-1, domain.CodeNotListening)}, nil, 3},
	}
	for _, c := range cases {
		if got := exitFor(app.Plan{Stop: domain.StopPlan{Results: c.results}}, c.outcomes); got != c.want {
			t.Errorf("%+v: %d, want %d", c.results, got, c.want)
		}
	}
}

// FuzzArguments proves no command line panics or stops anything by accident.
func FuzzArguments(f *testing.F) {
	f.Add("stop", "8000", "--json")
	f.Add("--yes", "kill", "9")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		fake := &fakeApp{plan: onePlan(), outcomes: []domain.Outcome{{}}}
		var out, errOut bytes.Buffer
		Run(t.Context(), []string{a, b, c}, Console{In: strings.NewReader(""), Out: &out, Err: &errOut}, fake)
		isConfirmed := slices.ContainsFunc([]string{a, b, c}, func(arg string) bool {
			return strings.HasPrefix(arg, "-") && strings.Contains(arg, "y")
		})
		if fake.executed > 0 && !isConfirmed {
			t.Fatalf("executed without confirmation: %q %q %q", a, b, c)
		}
	})
}

// TestManPageCoversEveryFlag keeps the manual complete.
func TestManPageCoversEveryFlag(t *testing.T) {
	data, err := os.ReadFile("../../resources/portly.1")
	if err != nil {
		t.Fatal(err)
	}
	manual := strings.ReplaceAll(string(data), `\-`, "-")
	for _, name := range []string{"help", "help-stop"} {
		help, err := os.ReadFile(filepath.Join("testdata", name+".golden"))
		if err != nil {
			t.Fatal(err)
		}
		for word := range strings.FieldsSeq(string(help)) {
			word = strings.TrimRight(word, `".`)
			if strings.HasPrefix(word, "--") && !strings.Contains(manual, word) {
				t.Errorf("%s is missing from the manual", word)
			}
		}
	}
}

// TestFitPath keeps the start and the last folders of a long path.
func TestFitPath(t *testing.T) {
	cases := []struct {
		path  string
		width int
		want  string
	}{
		{"~/shop", 12, "~/shop"},
		{"~/projects/clients/acme/web-app", 18, "~/…/acme/web-app"},
		{"~/projects/clients/acme/web-app", 12, "~/…/web-app"},
		{"/srv/apps/very-long-project-name", 14, "…-project-name"},
		{`~\projects\clients\acme\web-app`, 16, `~\…\acme\web-app`},
	}
	for _, c := range cases {
		if got := fitPath(c.path, c.width); got != c.want || len([]rune(got)) > c.width {
			t.Errorf("fitPath(%q, %d) = %q, want %q", c.path, c.width, got, c.want)
		}
	}
}

// TestLongDirectoryIsShortened fits a terminal and keeps the full path when piped.
func TestLongDirectoryIsShortened(t *testing.T) {
	long := "/home/dev/projects/clients/acme-corporation/backend/services/web-app"
	report := sampleReport()
	report.Ports[4].Owners[0].Process.Dir = long
	terminal := invoke(t, &fakeApp{report: report}, "", func(c *Console) { c.Width, c.Home = 120, "/home/dev" })
	if !strings.Contains(terminal.out, "~/…/") || !strings.Contains(terminal.out, "web-app") {
		t.Errorf("not shortened:\n%s", terminal.out)
	}
	piped := invoke(t, &fakeApp{report: report}, "", func(c *Console) { c.Home = "/home/dev" })
	if !strings.Contains(piped.out, "~/projects/clients/acme-corporation/backend/services/web-app") || strings.ContainsAny(piped.out, "│╭─") {
		t.Errorf("piped output changed:\n%s", piped.out)
	}
}

// TestFramedLinesLineUp draws every table line at one width, or plain columns when a frame can't fit.
func TestFramedLinesLineUp(t *testing.T) {
	for _, columns := range []int{40, 60, 80, 100, 120, 160} {
		got := invoke(t, &fakeApp{report: sampleReport()}, "", func(c *Console) { c.Width = columns })
		var widths []int
		for line := range strings.Lines(got.out) {
			if strings.ContainsAny(line, "│╭├╰") {
				widths = append(widths, len([]rune(strings.TrimRight(line, "\n"))))
			}
		}
		isPlain := len(widths) == 0 && columns < 45
		if !isPlain && (len(widths) == 0 || slices.Max(widths) != slices.Min(widths) || widths[0] > columns) {
			t.Errorf("%d columns: widths %v\n%s", columns, widths, got.out)
		}
	}
	dumb := invoke(t, &fakeApp{report: sampleReport()}, "", func(c *Console) { c.Width, c.Plain = 120, true })
	if strings.ContainsAny(dumb.out, "│╭├╰") {
		t.Errorf("TERM=dumb drew a frame:\n%s", dumb.out)
	}
}

// TestDirectoryOutlastsAddress keeps the project's folder when only one of the two fits.
func TestDirectoryOutlastsAddress(t *testing.T) {
	got := invoke(t, &fakeApp{report: sampleReport()}, "", func(c *Console) { c.Width = 80 })
	if !strings.Contains(got.out, "DIRECTORY") || strings.Contains(got.out, "ADDRESS") {
		t.Errorf("at 80 columns:\n%s", got.out)
	}
}

// TestUpdate asks before replacing the binary, and never prompts for JSON.
func TestUpdate(t *testing.T) {
	newer := app.UpdatePlan{Latest: "0.8.1", Path: "/home/dev/.local/bin/portly", IsNewer: true}
	release := func(c *Console) { c.Version, c.Home = "0.8.0", "/home/dev" }
	asking := func(c *Console) { release(c); interactive(c) }
	cases := []struct {
		name     string
		plan     app.UpdatePlan
		edit     func(*Console)
		input    string
		args     []string
		exit     int
		executed int
		out      string
	}{
		{"up to date", app.UpdatePlan{Latest: "0.8.0"}, release, "", nil, 0, 0, "portly 0.8.0 is up to date."},
		{"yes", newer, release, "", []string{"--yes"}, 0, 1, "Updated portly 0.8.0 → 0.8.1 in ~/.local/bin/portly"},
		{"no terminal", newer, release, "", nil, 6, 0, "portly 0.8.1 is available, you have 0.8.0."},
		{"asked yes", newer, asking, "y\n", nil, 0, 1, "Updated portly"},
		{"asked no", newer, asking, "n\n", nil, 6, 0, ""},
		{"json", newer, release, "", []string{"--json"}, 6, 0, `"status": "confirmation_required"`},
		{"json yes", newer, release, "", []string{"--json", "--yes"}, 0, 1, `"status": "updated"`},
		{"json up to date", app.UpdatePlan{Latest: "0.8.0"}, release, "", []string{"--json"}, 0, 0, `"status": "up_to_date"`},
	}
	for _, c := range cases {
		fake := &fakeApp{updatePlan: c.plan}
		got := invoke(t, fake, c.input, c.edit, append([]string{"update"}, c.args...)...)
		if got.exit != c.exit || fake.executed != c.executed || !strings.Contains(got.out, c.out) {
			t.Errorf("%s: exit %d, executed %d\n%s%s", c.name, got.exit, fake.executed, got.out, got.err)
		}
		if slices.Contains(c.args, "--json") {
			checkDocument(t, got.out)
		}
	}
}

// TestUpdateFailures keep their reason and say what to do next.
func TestUpdateFailures(t *testing.T) {
	denied := domain.NewError(domain.CodePermissionDenied, "Can't write to /opt/portly", "")
	release := func(c *Console) { c.Version, c.Executable = "0.8.0", "/opt/my apps/portly" }
	got := invoke(t, &fakeApp{err: denied}, "", release, "update")
	if got.exit != 4 || !strings.Contains(got.err, "sudo '/opt/my apps/portly' update") {
		t.Errorf("exit %d\n%s", got.exit, got.err)
	}
	failed := domain.NewError(domain.CodeChecksumMismatch, "bad download", "Try again")
	plan := app.UpdatePlan{Latest: "0.8.1", IsNewer: true}
	got = invoke(t, &fakeApp{updatePlan: plan, updateErr: failed}, "", release, "update", "--yes", "--json")
	if document := checkDocument(t, got.out); got.exit != 1 || document["error"].(map[string]any)["code"] != "checksum_mismatch" {
		t.Errorf("exit %d\n%s", got.exit, got.out)
	}
	windows := func(c *Console) { release(c); c.Windows = true }
	if got := invoke(t, &fakeApp{err: denied}, "", windows, "update"); !strings.Contains(got.err, "administrator terminal") || strings.Contains(got.err, "sudo") {
		t.Errorf("windows hint:\n%s", got.err)
	}
	cancelled := invoke(t, &fakeApp{err: context.Canceled}, "", release, "update")
	if cancelled.exit != 6 || !strings.Contains(cancelled.err, "Nothing was changed") {
		t.Errorf("cancel: exit %d\n%s", cancelled.exit, cancelled.err)
	}
	if got := invoke(t, &fakeApp{}, "", release, "update", "extra"); got.exit != 2 {
		t.Errorf("arguments: exit %d", got.exit)
	}
}
