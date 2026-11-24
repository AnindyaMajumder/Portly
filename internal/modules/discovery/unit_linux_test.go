package discovery

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// TestParseMainPID proves 0 and noise mean unknown.
func TestParseMainPID(t *testing.T) {
	if pid, ok := parseMainPID([]byte("42\n")); !ok || pid != 42 {
		t.Errorf("got %d %v", pid, ok)
	}
	for _, bad := range []string{"", "0\n", "-5", "x", "1 2", "11111111111111111111111111111111111111111111111111111111111111111"} {
		if _, ok := parseMainPID([]byte(bad)); ok {
			t.Errorf("%q should be unknown", bad)
		}
	}
}

// TestHostileUnitNamesAreNeverAsked covers options, shells and other users.
func TestHostileUnitNamesAreNeverAsked(t *testing.T) {
	asked := 0
	source := source{mainPID: func(context.Context, domain.Unit, string) (int, bool) { asked++; return 7, true }}
	process := func(name string, isUser bool) domain.Process {
		found := domain.Process{Unit: domain.Unit{Name: name, User: isUser, Kind: domain.UnitService}}
		found.UserID = "1000"
		return found
	}
	for _, name := range []string{"", "-H evil.service", "a b.service", "$(id).service", "a;b.service", "a/b.service"} {
		if unit := source.withMainPID(t.Context(), process(name, false), "1000"); unit.MainKnown {
			t.Errorf("%q was asked", name)
		}
	}
	if unit := source.withMainPID(t.Context(), process("web.service", true), "0"); unit.MainKnown {
		t.Error("another user's manager was asked")
	}
	if asked != 0 {
		t.Fatalf("asked %d times", asked)
	}
	if unit := source.withMainPID(t.Context(), process(`web\x2dapp@1.service`, true), "1000"); unit.MainPID != 7 || !unit.MainKnown {
		t.Errorf("got %+v", unit)
	}
}

// TestAddManagersAsksOncePerUnit covers owners of named endpoints only.
func TestAddManagersAsksOncePerUnit(t *testing.T) {
	fake := newFakeProc(t)
	unit := "/system.slice/web.service"
	fake.listen(8000, "1000", "77")
	fake.listen(9000, "1000", "78")
	fake.process(fakeProcess{pid: 600, ppid: 1, uid: "1000", start: 60, inodes: []string{"77"}, cgroup: unit})
	fake.process(fakeProcess{pid: 601, ppid: 600, uid: "1000", start: 61, inodes: []string{"77"}, cgroup: unit})
	fake.process(fakeProcess{pid: 602, ppid: 1, uid: "1000", start: 62, inodes: []string{"78"}, cgroup: "/system.slice/other.service"})
	source, asked := fake.source(), 0
	source.mainPID = func(context.Context, domain.Unit, string) (int, bool) { asked++; return 600, true }
	snapshot := scanFake(t, fake)
	source.addManagers(t.Context(), &snapshot, []domain.Endpoint{port8000})
	if asked != 1 || snapshot.Processes[601].Unit.MainPID != 600 || snapshot.Processes[602].Unit.MainKnown {
		t.Errorf("asked %d, got %+v", asked, snapshot.Processes[601].Unit)
	}
}

// TestSystemctlUnknownUnit asks the real manager about a unit that can't exist.
func TestSystemctlUnknownUnit(t *testing.T) {
	if !exists("/run/systemd/system") {
		t.Skip("no systemd here")
	}
	unit := domain.Unit{Name: "portly-test-does-not-exist.service", Kind: domain.UnitService}
	if pid, ok := systemctlMainPID(t.Context(), unit, strconv.Itoa(os.Getuid())); ok {
		t.Errorf("got %d", pid)
	}
}
