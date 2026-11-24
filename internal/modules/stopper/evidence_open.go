//go:build portly_evidence

package stopper

import (
	"slices"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// EvidenceBuild is true only in builds made to test real stops.
const EvidenceBuild = true

// Compose project label every test container carries
const testProject = "portly-test"

// withEvidence opens the gates, but only for what the test started.
func withEvidence(platform gates) gates {
	platform.native, platform.docker, platform.podman, platform.guard = true, true, true, startedByTest
	return platform
}

// startedByTest accepts descendants of the test binary and labelled containers.
func startedByTest(snapshot domain.Snapshot, candidate candidate) bool {
	if candidate.target.Kind == domain.KindContainer {
		return candidate.container.ComposeProject == testProject
	}
	parent := snapshot.Processes[snapshot.Processes[snapshot.Host.SelfPID].PPID]
	isTest := parent.PID > 1 && !parent.UserManager && strings.HasSuffix(parent.Exe, ".test")
	ancestors, isComplete := snapshot.Ancestors(candidate.target.Root.PID)
	isDescendant := slices.ContainsFunc(ancestors, func(ancestor domain.Process) bool { return ancestor.PID == parent.PID })
	return isComplete && isTest && isDescendant
}
