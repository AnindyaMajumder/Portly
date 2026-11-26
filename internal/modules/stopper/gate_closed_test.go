//go:build !portly_evidence && !(linux && (amd64 || arm64))

package stopper

import (
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// TestReleaseGatesAreClosed changes only together with recorded evidence.
func TestReleaseGatesAreClosed(t *testing.T) {
	built := New(nil, nil)
	if EvidenceBuild || built.gates.native || built.gates.docker || built.gates.guard != nil {
		t.Errorf("an unproven gate is open: %+v", built.gates)
	}
	request := domain.StopRequest{Endpoints: []domain.Endpoint{web}, Force: true}
	if plan := built.Decide(world(), request); len(plan.Targets) != 0 {
		t.Errorf("a release build allowed %+v", plan.Targets)
	}
}
