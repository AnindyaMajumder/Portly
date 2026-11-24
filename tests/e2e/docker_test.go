//go:build linux

package e2e

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	// Already on the machine, never pulled
	image = "redis:7-alpine"
	// Every container ends and is removed by itself
	obedient = `trap "exit 0" TERM; sleep 45 & wait`
	stubborn = `trap "" TERM; sleep 45`
)

// container is a throwaway container of this test.
type container struct{ id, port string }

// needsDocker skips unless asked for, and unless the image is local.
func needsDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("PORTLY_DOCKER_TESTS") == "" {
		t.Skip("set PORTLY_DOCKER_TESTS=1 to use throwaway containers")
	}
	if exec.Command("docker", "image", "inspect", image).Run() != nil {
		t.Skip(image + " is not available locally")
	}
}

// freePort picks a loopback port nothing uses.
func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return portOf(listener)
}

// runContainer starts a limited, labelled, self-removing container.
func runContainer(t *testing.T, label, script string, flags ...string) container {
	t.Helper()
	needsDocker(t)
	c := container{port: freePort(t)}
	arguments := []string{
		"run", "-d", "--rm", "--pull=never", "--memory=64m", "--pids-limit=32", "--cpus=0.5",
		"--label", "com.docker.compose.project=" + label, "-p", "127.0.0.1:" + c.port + ":80",
	}
	arguments = append(append(arguments, flags...), image, "sh", "-c", strings.ReplaceAll(script, "PORT", c.port))
	output, err := exec.Command("docker", arguments...).Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	c.id = strings.TrimSpace(string(output))
	return c
}

// running asks the engine about this test's container only.
func (c container) running() bool {
	output, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", c.id).Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

// TestDockerLookup resolves a published port to its container.
func TestDockerLookup(t *testing.T) {
	c := runContainer(t, "portly-test", obedient)
	got := portly(t, release, c.port, "--json")
	if got.exit != 0 || !strings.Contains(got.stdout, c.id) || !strings.Contains(got.stdout, `"local": "proven"`) {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
}

// TestDockerReleaseRefuses is the closed gate.
func TestDockerReleaseRefuses(t *testing.T) {
	c := runContainer(t, "portly-test", obedient)
	got := portly(t, release, "stop", c.port, "--yes", "--force", "--json")
	if got.exit != 5 || got.json.Results[0].Error.Code != "platform_unverified" || !c.running() {
		t.Errorf("exit %d, running %v\n%s", got.exit, c.running(), got.stdout)
	}
}

// TestDockerGuard proves even the evidence build leaves other projects alone.
func TestDockerGuard(t *testing.T) {
	c := runContainer(t, "portly-guard", obedient)
	if got := stop(t, c.port, "--force"); got.exit != 5 || got.json.Results[0].Error.Code != "platform_unverified" || !c.running() {
		t.Errorf("exit %d, running %v\n%s", got.exit, c.running(), got.stdout)
	}
}

// TestDockerGracefulStop goes through the engine.
func TestDockerGracefulStop(t *testing.T) {
	c := runContainer(t, "portly-test", obedient)
	got := stop(t, c.port)
	got.expect(t, 0, "stopped")
	if c.running() || got.json.Targets[0].Kind != "container" || got.json.Targets[0].Outcome.Method != "graceful" {
		t.Errorf("running %v, %+v", c.running(), got.json.Targets[0])
	}
}

// TestDockerNextToAClosedEngine covers a machine that also has root's Podman.
func TestDockerNextToAClosedEngine(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("nothing is closed to root")
	}
	closedEngine[t.Name()] = true
	defer delete(closedEngine, t.Name())
	c := runContainer(t, "portly-test", obedient)
	stop(t, c.port).expect(t, 0, "stopped")
	s := start(t, "tcp")
	stop(t, s.ports[0]).expect(t, 0, "stopped")
}

// TestDockerRouted covers a network mode without host sockets.
func TestDockerRouted(t *testing.T) {
	network := os.Getenv("PORTLY_ROUTED_NETWORK")
	if network == "" {
		t.Skip("gate not proven here: set PORTLY_ROUTED_NETWORK")
	}
	c := runContainer(t, "portly-test", obedient, "--network="+network)
	got := stop(t, c.port)
	t.Logf("exit %d\n%s", got.exit, got.stdout)
	if got.exit == 3 {
		if !c.running() {
			t.Error("not listening, yet the container is gone")
		}
		return
	}
	got.expect(t, 0, "stopped")
	if c.running() || got.json.Targets[0].Kind != "container" {
		t.Errorf("running %v, %+v", c.running(), got.json.Targets[0])
	}
}

// TestDockerIgnoredStopIsPending proves t=-1 never ends in a kill.
func TestDockerIgnoredStopIsPending(t *testing.T) {
	c := runContainer(t, "portly-test", stubborn)
	started := time.Now()
	got := stop(t, c.port)
	got.expect(t, 7, "pending")
	if !c.running() || time.Since(started) < 10*time.Second {
		t.Errorf("running %v after %v", c.running(), time.Since(started))
	}
	// The engine's own default would have killed it by now
	time.Sleep(3 * time.Second)
	if !c.running() {
		t.Error("the engine killed the container")
	}
}

// TestDockerForce kills only after the graceful stop failed.
func TestDockerForce(t *testing.T) {
	c := runContainer(t, "portly-test", stubborn)
	got := stop(t, c.port, "--force")
	got.expect(t, 0, "stopped")
	if c.running() || got.json.Targets[0].Outcome.Method != "forced" {
		t.Errorf("running %v, %+v", c.running(), got.json.Targets[0].Outcome)
	}
}

// TestDockerKillStopSignal needs --force, because nothing graceful exists.
func TestDockerKillStopSignal(t *testing.T) {
	for _, signal := range []string{"SIGKILL", "9"} {
		c := runContainer(t, "portly-test", obedient, "--stop-signal", signal)
		if got := stop(t, c.port); got.exit != 5 || got.json.Results[0].Error.Code != "no_graceful_method" || !c.running() {
			t.Errorf("%s: exit %d, running %v\n%s", signal, got.exit, c.running(), got.stdout)
		}
	}
}

// TestDockerNativeIntruder refuses a published port shared with a native socket.
func TestDockerNativeIntruder(t *testing.T) {
	c := runContainer(t, "portly-test", obedient)
	listener, err := net.Listen("tcp", "127.0.0.2:"+c.port)
	if err != nil {
		t.Skip(err)
	}
	defer listener.Close()
	if got := stop(t, c.port, "--force"); got.exit != 5 || got.json.Results[0].Error.Code != "ambiguous_owner" || !c.running() {
		t.Errorf("exit %d, running %v\n%s", got.exit, c.running(), got.stdout)
	}
}

// TestDockerHostNetwork stops the container, not the process inside it.
func TestDockerHostNetwork(t *testing.T) {
	needsDocker(t)
	script := `trap "kill %1; exit 0" TERM; nc -lk -s 127.0.0.1 -p ` + freePort(t) + ` & sleep 45 & wait %2`
	port := strings.Fields(strings.SplitN(script, "-p ", 2)[1])[0]
	output, err := exec.Command("docker", "run", "-d", "--rm", "--pull=never", "--memory=64m", "--pids-limit=32", "--network=host",
		"--label", "com.docker.compose.project=portly-test", image, "sh", "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	c := container{id: strings.TrimSpace(string(output)), port: port}
	for range 50 {
		if !isFree(port) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	got := portly(t, evidence, "stop", port, "--json")
	// A root process inside is hidden from a normal user, so refusing is right too
	if os.Geteuid() != 0 {
		if got.exit != 4 || !c.running() {
			t.Errorf("exit %d, running %v\n%s", got.exit, c.running(), got.stdout)
		}
		return
	}
	stop(t, port).expect(t, 0, "stopped")
}
