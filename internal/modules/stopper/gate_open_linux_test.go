//go:build !portly_evidence && (amd64 || arm64)

package stopper

import (
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// TestLinuxGateIsOpen allows a proven target in a release build, with no test-only guard.
func TestLinuxGateIsOpen(t *testing.T) {
	built := New(nil, nil)
	if EvidenceBuild || !built.gates.native || !built.gates.docker || built.gates.guard != nil || !built.Enabled() {
		t.Errorf("the Linux gate is not open as recorded: %+v", built.gates)
	}
	plan := built.Decide(world(), domain.StopRequest{Endpoints: []domain.Endpoint{web}})
	if len(plan.Targets) != 1 {
		t.Errorf("a proven target was refused: %+v", plan)
	}
}
