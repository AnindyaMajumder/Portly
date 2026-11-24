//go:build linux

package e2e

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// Directory with ready binaries, for runs without a Go toolchain
	binVariable = "PORTLY_E2E_BIN"
	// Says what a run inside a container must find
	insideVariable = "PORTLY_E2E_INSIDE"
)

// insideTests are the stops that must behave the same in every environment.
const insideTests = "TestInside|TestGracefulStop$|TestNothingStops|TestIgnoredSignal|TestForceKills|TestSuspendedIs|TestTwoOwners|" +
	"TestUDPNeeds|TestFamily$|TestStubbornFamily|TestOwnAncestor|TestSupervisorIs|TestForwarderIs|TestRestartIs"

// runInside runs the tests again in a throwaway container without capabilities.
func runInside(t *testing.T, kind, pattern string, flags ...string) {
	t.Helper()
	needsDocker(t)
	dir := filepath.Dir(release)
	tests := filepath.Join(dir, "e2e.test")
	build := exec.Command("go", "test", "-c", "-o", tests, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	arguments := []string{
		"run", "--rm", "--init", "--pull=never", "--network=none", "--memory=512m", "--pids-limit=128", "--cpus=2",
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--tmpfs=/tmp", "--workdir=/tmp",
		"--label", "com.docker.compose.project=portly-test", "-v", dir + ":/portly:ro",
		"-e", binVariable + "=/portly", "-e", insideVariable + "=" + kind,
	}
	arguments = append(append(arguments, flags...), image, "timeout", "150", "/portly/e2e.test", "-test.v", "-test.timeout=140s", "-test.run="+pattern)
	output, err := exec.Command("docker", arguments...).CombinedOutput()
	t.Log("\n" + string(output))
	if err != nil || !strings.Contains(string(output), "--- PASS: TestInside") {
		t.Errorf("%v\n%s", err, output)
	}
}

// TestDockerInsideNamespaces covers other namespaces and no systemd.
func TestDockerInsideNamespaces(t *testing.T) {
	runInside(t, "user", insideTests, "--user=1000:1000")
}

// TestDockerInsideWithoutPidfd covers kernels before 5.3.
func TestDockerInsideWithoutPidfd(t *testing.T) {
	profile, err := filepath.Abs("testdata/no-pidfd.json")
	if err != nil {
		t.Fatal(err)
	}
	runInside(t, "no-pidfd", insideTests, "--user=1000:1000", "--security-opt=seccomp="+profile)
}

// TestDockerInsideAsRoot covers system accounts with and without a terminal.
func TestDockerInsideAsRoot(t *testing.T) {
	runInside(t, "root", "TestInside")
	runInside(t, "root-terminal", "TestInside|TestGracefulStop$|TestForceKills", "--tty")
}

// TestDockerOtherNetwork never mixes up equal port numbers.
func TestDockerOtherNetwork(t *testing.T) {
	needsDocker(t)
	s := start(t, "tcp")
	// Ends by itself, and takes the listener with it
	script := "exec timeout 45 nc -lk -p " + s.ports[0]
	output, err := exec.Command("docker", "run", "-d", "--rm", "--pull=never", "--memory=64m", "--pids-limit=32",
		"--label", "com.docker.compose.project=portly-test", image, "sh", "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	c := container{id: strings.TrimSpace(string(output))}
	time.Sleep(500 * time.Millisecond)
	got := stop(t, s.ports[0])
	got.expect(t, 0, "stopped")
	if got.json.Targets[0].Kind != "process" || !c.running() {
		t.Errorf("%+v, container running %v", got.json.Targets[0], c.running())
	}
	stop(t, s.ports[0]).expect(t, 3, "not_listening")
}

// TestInsideEnvironment proves the container really has the named properties.
func TestInsideEnvironment(t *testing.T) {
	kind := os.Getenv(insideVariable)
	if kind == "" {
		t.Skip("only inside a test container")
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		t.Error("systemd is here")
	}
	if namespace, err := os.Readlink("/proc/self/ns/pid"); err != nil || namespace == "pid:[4026531836]" {
		t.Errorf("not another namespace: %q %v", namespace, err)
	}
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err == nil {
		_ = unix.Close(fd)
	}
	if (kind == "no-pidfd") != errors.Is(err, unix.ENOSYS) {
		t.Errorf("%s: pidfd_open says %v", kind, err)
	}
	if (os.Geteuid() == 0) != strings.HasPrefix(kind, "root") {
		t.Errorf("%s: running as %d", kind, os.Geteuid())
	}
}

// TestInsideRootWithoutTerminal is the system account rule.
func TestInsideRootWithoutTerminal(t *testing.T) {
	if os.Getenv(insideVariable) != "root" {
		t.Skip("only as root inside a test container")
	}
	s := start(t, "tcp")
	if got := stop(t, s.ports[0], "--force"); got.exit != 5 || got.json.Results[0].Error.Code != "protected_process" || !s.alive() {
		t.Errorf("exit %d, alive %v\n%s", got.exit, s.alive(), got.stdout)
	}
}

// TestHiddenProcesses needs /proc mounted with hidepid, which only CI does.
func TestHiddenProcesses(t *testing.T) {
	if _, err := os.ReadFile("/proc/1/stat"); err == nil {
		t.Skip("gate not proven here: /proc shows every process")
	}
	s := start(t, "tcp")
	if got := portly(t, release, s.ports[0], "--json"); got.exit != 0 || got.json.Ports[0]["status"] != "listening" {
		t.Errorf("lookup: exit %d\n%s", got.exit, got.stdout)
	}
	got := stop(t, s.ports[0], "--force")
	if got.exit != 4 || got.json.Results[0].Error.Code != "evidence_unavailable" || !s.alive() {
		t.Errorf("exit %d, alive %v\n%s", got.exit, s.alive(), got.stdout)
	}
}
