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

// TestPodmanLookup resolves a published port to its container.
func TestPodmanLookup(t *testing.T) {
	c := runPodman(t, obedient)
	var got result
	// The first answers of a new service are slow
	for range 10 {
		got = portly(t, release, c.port, "--json")
		if strings.Contains(got.stdout, c.id) {
			break
		}
		time.Sleep(time.Second)
	}
	if got.exit != 0 || !strings.Contains(got.stdout, c.id) || !strings.Contains(got.stdout, `"type": "Podman"`) {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
	t.Log(podman(t, "version", "--format", "{{.Client.Version}}"), "proven local:", strings.Contains(got.stdout, `"local": "proven"`))
}

// TestPodmanIsNeverStopped proves nothing is sent, because t=-1 can end in a kill.
func TestPodmanIsNeverStopped(t *testing.T) {
	for _, script := range []string{obedient, stubborn} {
		c := runPodman(t, script)
		for _, flags := range [][]string{nil, {"--force"}} {
			got := stop(t, append([]string{c.port}, flags...)...)
			if got.exit != 5 || got.json.Results[0].Error.Code != "engine_rejected_stop" || !podmanRunning(c) {
				t.Errorf("%v: exit %d, running %v\n%s", flags, got.exit, podmanRunning(c), got.stdout)
			}
		}
	}
}
