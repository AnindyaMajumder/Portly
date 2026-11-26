//go:build linux

package e2e

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestReleaseBuildStops is the open Linux gate: the build users get stops a server this test started.
func TestReleaseBuildStops(t *testing.T) {
	s := start(t, "tcp")
	got := portly(t, release, "stop", s.ports[0], "--yes", "--json")
	got.expect(t, 0, "stopped")
	if s.alive() || !isFree(s.ports[0]) {
		t.Errorf("alive %v\n%s", s.alive(), got.stdout)
	}
}

// TestReleaseBuildStillRefuses keeps every safety rule without the test guard.
func TestReleaseBuildStillRefuses(t *testing.T) {
	first := start(t, "share")
	second := start(t, "share", "PORTLY_E2E_PORT="+first.ports[0])
	got := portly(t, release, "stop", first.ports[0], "--yes", "--force", "--json")
	if got.exit != 5 || got.json.Results[0].Error.Code != "ambiguous_owner" || !first.alive() || !second.alive() {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
}

// TestGracefulStop frees the port and reports how.
func TestGracefulStop(t *testing.T) {
	s := start(t, "tcp")
	got := stop(t, s.ports[0])
	got.expect(t, 0, "stopped")
	if s.alive() || !isFree(s.ports[0]) || got.json.Targets[0].Outcome.Method != "graceful" {
		t.Errorf("alive %v, %+v", s.alive(), got.json.Targets[0].Outcome)
	}
}

// TestNothingStopsWithoutConfirmation covers pipes, JSON and force.
func TestNothingStopsWithoutConfirmation(t *testing.T) {
	s := start(t, "tcp")
	for _, args := range [][]string{{"stop", s.ports[0]}, {"stop", s.ports[0], "--json"}, {"stop", s.ports[0], "--force"}} {
		if got := portly(t, evidence, args...); got.exit != 6 || !s.alive() {
			t.Errorf("%v: exit %d, alive %v", args, got.exit, s.alive())
		}
	}
}

// TestIgnoredSignalSurvives proves nothing is killed without --force.
func TestIgnoredSignalSurvives(t *testing.T) {
	s := start(t, "ignore")
	got := stop(t, s.ports[0])
	got.expect(t, 7, "still_listening")
	if !s.alive() || len(got.json.Targets[0].Outcome.KilledPIDs) != 0 {
		t.Error("a process was killed without --force")
	}
}

// TestForceKillsAfterAFailedStop reports the kill.
func TestForceKillsAfterAFailedStop(t *testing.T) {
	s := start(t, "ignore")
	got := stop(t, s.ports[0], "--force")
	got.expect(t, 0, "stopped")
	outcome := got.json.Targets[0].Outcome
	if outcome.Method != "forced" || !slices.Equal(outcome.KilledPIDs, []int{s.pid()}) || !isFree(s.ports[0]) {
		t.Errorf("%+v", outcome)
	}
}

// TestForceIsNotUsedWhenGracefulWorks keeps the method graceful.
func TestForceIsNotUsedWhenGracefulWorks(t *testing.T) {
	s := start(t, "tcp")
	if got := stop(t, s.ports[0], "--force"); got.json.Targets[0].Outcome.Method != "graceful" {
		t.Errorf("%+v", got.json.Targets[0].Outcome)
	}
}

// TestRestartIsNeverSuccess covers a respawning parent.
func TestRestartIsNeverSuccess(t *testing.T) {
	s := start(t, "respawn")
	stop(t, s.ports[0]).expect(t, 7, "restarted")
}

// TestSuspendedIsResumedToStop covers Ctrl-Z.
func TestSuspendedIsResumedToStop(t *testing.T) {
	s := start(t, "tcp")
	if err := s.command.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	got := stop(t, s.ports[0])
	got.expect(t, 0, "stopped")
	if !got.json.Targets[0].Outcome.Resumed {
		t.Error("resume was not reported")
	}
}

// TestSuspendedAndIgnoringIsRefused would never handle the signal.
func TestSuspendedAndIgnoringIsRefused(t *testing.T) {
	s := start(t, "ignore")
	if err := s.command.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := stop(t, s.ports[0], "--force"); got.exit != 5 || got.json.Results[0].Error.Code != "stop_signal_blocked" || !s.alive() {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
}

// TestDualStackIsOneTarget stops both sockets.
func TestDualStackIsOneTarget(t *testing.T) {
	s := start(t, "dual")
	got := stop(t, s.ports[0])
	got.expect(t, 0, "stopped")
	if len(got.json.Targets) != 1 {
		t.Errorf("%d targets", len(got.json.Targets))
	}
}

// TestTwoOwnersAreRefused leaves both running.
func TestTwoOwnersAreRefused(t *testing.T) {
	first := start(t, "share")
	second := start(t, "share", "PORTLY_E2E_PORT="+first.ports[0])
	got := stop(t, first.ports[0], "--force")
	if got.exit != 5 || got.json.Results[0].Error.Code != "ambiguous_owner" || !first.alive() || !second.alive() {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
}

// TestSharedUDPIsRefused keeps both owners of a shared UDP port, like mDNS on 5353.
func TestSharedUDPIsRefused(t *testing.T) {
	first := start(t, "udp-share")
	second := start(t, "udp-share", "PORTLY_E2E_PORT="+first.ports[0])
	got := stop(t, first.ports[0]+"/udp", "--force")
	if got.exit != 5 || got.json.Results[0].Error.Code != "ambiguous_owner" || !first.alive() || !second.alive() {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
}

// TestUDPNeedsToBeNamed proves a bare port never stops UDP.
func TestUDPNeedsToBeNamed(t *testing.T) {
	s := start(t, "udp")
	got := stop(t, s.ports[0])
	got.expect(t, 3, "not_listening")
	if !s.alive() || !strings.Contains(got.json.Results[0].Error.Hint, "/udp") {
		t.Errorf("alive %v, %+v", s.alive(), got.json.Results[0].Error)
	}
	stop(t, s.ports[0]+"/udp").expect(t, 0, "stopped")
}

// TestSameNumberTCPAndUDP never mixes the two.
func TestSameNumberTCPAndUDP(t *testing.T) {
	tcp := start(t, "tcp")
	udp := start(t, "udp", "PORTLY_E2E_PORT="+tcp.ports[0])
	stop(t, tcp.ports[0]).expect(t, 0, "stopped")
	if !udp.alive() {
		t.Error("the UDP owner was stopped")
	}
}

// TestSeveralPorts covers success, mixes, duplicates and a shared owner.
func TestSeveralPorts(t *testing.T) {
	first, second, both := start(t, "tcp"), start(t, "tcp"), start(t, "two-ports")
	got := stop(t, first.ports[0], second.ports[0], first.ports[0])
	if got.exit != 0 || len(got.json.Results) != 2 || first.alive() || second.alive() {
		t.Errorf("two: exit %d\n%s", got.exit, got.stdout)
	}
	got = stop(t, both.ports[0], both.ports[1], first.ports[0])
	if got.exit != 8 || len(got.json.Targets) != 1 || got.json.Results[2].Status != "not_listening" || both.alive() {
		t.Errorf("mixed: exit %d\n%s", got.exit, got.stdout)
	}
}

// TestOtherEndpointsAreShown before anything stops.
func TestOtherEndpointsAreShown(t *testing.T) {
	s := start(t, "two-ports")
	got := portly(t, evidence, "stop", s.ports[0], "--json")
	if affected := got.json.Targets[0].Affected; len(affected) != 1 || strconv.Itoa(affected[0].Port) != s.ports[1] {
		t.Errorf("%+v", affected)
	}
}

// TestFamily stops a prefork server through its root.
func TestFamily(t *testing.T) {
	s := start(t, "family")
	got := stop(t, s.ports[0])
	got.expect(t, 0, "stopped")
	if got.json.Targets[0].Kind != "family" || s.alive() {
		t.Errorf("%+v", got.json.Targets[0])
	}
}

// TestStubbornFamily needs force, and kills only its holders.
func TestStubbornFamily(t *testing.T) {
	s := start(t, "stubborn-family")
	stop(t, s.ports[0]).expect(t, 7, "still_listening")
	got := stop(t, s.ports[0], "--force")
	got.expect(t, 0, "stopped")
	if killed := got.json.Targets[0].Outcome.KilledPIDs; len(killed) != 3 || killed[0] != s.pid() {
		t.Errorf("killed %v, root %d", killed, s.pid())
	}
}

// TestExpect accepts the same target and refuses a replaced one.
func TestExpect(t *testing.T) {
	s := start(t, "share")
	id := portly(t, evidence, "stop", s.ports[0], "--json").json.PlanID
	if len(id) != 32 {
		t.Fatalf("plan id %q", id)
	}
	_ = s.command.Process.Kill()
	<-s.exited
	replacement := start(t, "share", "PORTLY_E2E_PORT="+s.ports[0])
	got := stop(t, s.ports[0], "--expect", id)
	if got.exit != 5 || got.json.Error == nil || got.json.Error.Code != "target_changed" || !replacement.alive() {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
	id = portly(t, evidence, "stop", s.ports[0], "--json").json.PlanID
	stop(t, s.ports[0], "--expect", id).expect(t, 0, "stopped")
}

// TestOwnAncestorIsProtected covers a port of the process that launched Portly.
func TestOwnAncestorIsProtected(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if got := stop(t, portOf(listener), "--force"); got.exit != 5 || got.json.Results[0].Error.Code != "protected_process" {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
}

// TestSupervisorIsRespected covers a parent that would restart its child.
func TestSupervisorIsRespected(t *testing.T) {
	link := filepath.Join(t.TempDir(), "nodemon")
	if err := os.Symlink(os.Args[0], link); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(link)
	command.Env = append(os.Environ(), modeVariable+"=parent")
	s := launch(t, command)
	if got := stop(t, s.ports[0], "--force"); got.exit != 5 || got.json.Results[0].Error.Code != "supervised_process" || !s.alive() {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
}

// TestForwarderIsRefused covers a process that only forwards.
func TestForwarderIsRefused(t *testing.T) {
	link := filepath.Join(t.TempDir(), "docker-proxy")
	if err := os.Symlink(os.Args[0], link); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(link)
	command.Env = append(os.Environ(), modeVariable+"=tcp")
	s := launch(t, command)
	if got := stop(t, s.ports[0], "--force"); got.exit != 5 || got.json.Results[0].Error.Code != "port_forwarder" || !s.alive() {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
}

// TestLookup finds the server and a free port.
func TestLookup(t *testing.T) {
	s := start(t, "tcp")
	got := portly(t, release, s.ports[0], "--json")
	if got.exit != 0 || got.json.Ports[0]["status"] != "listening" || !strings.Contains(got.stdout, strconv.Itoa(s.pid())) {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
	_ = s.command.Process.Kill()
	<-s.exited
	if got := portly(t, release, s.ports[0], "--json"); got.exit != 0 || got.json.Ports[0]["status"] != "not_listening" {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
}

// TestRepeatedStops covers PID churn, one stop at a time.
func TestRepeatedStops(t *testing.T) {
	cycles, err := strconv.Atoi(os.Getenv("PORTLY_STOP_CYCLES"))
	if err != nil {
		cycles = 3
	}
	for range cycles {
		s := start(t, "tcp")
		stop(t, s.ports[0]).expect(t, 0, "stopped")
	}
}
