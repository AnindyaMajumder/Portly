//go:build linux

package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// podman runs one podman command.
func podman(t *testing.T, arguments ...string) string {
	t.Helper()
	output, err := exec.Command("podman", arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

// runPodman starts a throwaway container that ends by itself.
func runPodman(t *testing.T, script string) container {
	t.Helper()
	if os.Getenv("PORTLY_PODMAN_TESTS") == "" {
		t.Skip("gate not proven here: set PORTLY_PODMAN_TESTS=1 with a running podman service")
	}
	c := container{port: freePort(t)}
	c.id = podman(t, "run", "-d", "--rm", "--label", "com.docker.compose.project=portly-test",
		"-p", "127.0.0.1:"+c.port+":80", "docker.io/library/"+image, "sh", "-c", script)
	return c
}

// podmanRunning asks about this test's container only.
func podmanRunning(c container) bool {
	output, err := exec.Command("podman", "inspect", "-f", "{{.State.Running}}", c.id).Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

// isRejected reports an engine that does not accept t=-1.
func isRejected(r result) bool {
	return r.exit == 5 && r.json.Results[0].Error != nil && r.json.Results[0].Error.Code == "engine_rejected_stop"
}

// TestPodmanLookup resolves a published port to its container.
func TestPodmanLookup(t *testing.T) {
	c := runPodman(t, obedient)
	got := portly(t, release, c.port, "--json")
	if got.exit != 0 || !strings.Contains(got.stdout, c.id) || !strings.Contains(got.stdout, `"type": "Podman"`) {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
	t.Log(strings.Contains(got.stdout, `"local": "proven"`), podman(t, "version", "--format", "{{.Client.Version}}"))
}

// TestPodmanStop stops the container, or leaves it alone when t=-1 is rejected.
func TestPodmanStop(t *testing.T) {
	c := runPodman(t, obedient)
	got := stop(t, c.port)
	if isRejected(got) && podmanRunning(c) {
		t.Log("this version rejects t=-1, nothing was stopped")
		return
	}
	got.expect(t, 0, "stopped")
	if podmanRunning(c) || got.json.Targets[0].Kind != "container" {
		t.Errorf("%+v", got.json.Targets[0])
	}
}

// TestPodmanNeverKills proves t=-1 does not escalate.
func TestPodmanNeverKills(t *testing.T) {
	c := runPodman(t, stubborn)
	got := stop(t, c.port)
	if !isRejected(got) {
		got.expect(t, 7, "pending")
	}
	time.Sleep(3 * time.Second)
	if !podmanRunning(c) {
		t.Error("the engine killed the container")
	}
}

// TestPodmanForce kills only after a delivered stop failed.
func TestPodmanForce(t *testing.T) {
	c := runPodman(t, stubborn)
	got := stop(t, c.port, "--force")
	if isRejected(got) {
		if !podmanRunning(c) {
			t.Error("a rejected stop became a kill")
		}
		return
	}
	got.expect(t, 0, "stopped")
	if podmanRunning(c) || got.json.Targets[0].Outcome.Method != "forced" {
		t.Errorf("%+v", got.json.Targets[0].Outcome)
	}
}
