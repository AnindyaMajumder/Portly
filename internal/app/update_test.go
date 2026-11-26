package app

import (
	"context"
	"errors"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// fakeUpdater answers with fixed values and records the install.
type fakeUpdater struct {
	latest     string
	latestErr  error
	targetErr  error
	installed  string
	targetUsed bool
}

// Latest returns the fixed release.
func (f *fakeUpdater) Latest(context.Context) (string, error) { return f.latest, f.latestErr }

// Target returns a fixed path.
func (f *fakeUpdater) Target() (string, error) {
	f.targetUsed = true
	return "/home/dev/.local/bin/portly", f.targetErr
}

// Install records what would be installed where.
func (f *fakeUpdater) Install(_ context.Context, version, path string) error {
	f.installed = version + " " + path
	return nil
}

// TestCheckUpdate updates only release builds to newer releases.
func TestCheckUpdate(t *testing.T) {
	refused := domain.NewError(domain.CodePackageManaged, "packaged", "")
	cases := []struct {
		name, current, latest string
		targetErr             error
		newer                 bool
		code                  domain.Code
	}{
		{"newer", "0.8.0", "0.8.1", nil, true, ""},
		{"newer minor", "0.8.9", "0.10.0", nil, true, ""},
		{"same", "0.8.1", "0.8.1", nil, false, ""},
		{"older", "1.0.0", "0.9.9", nil, false, ""},
		{"dev", "e3d0888", "0.8.1", nil, false, domain.CodeDevBuild},
		{"tag", "v0.8.0", "0.8.1", nil, false, domain.CodeDevBuild},
		{"evidence", "0.8.0 (evidence build, for tests only)", "0.8.1", nil, false, domain.CodeDevBuild},
		{"packaged", "0.8.0", "0.8.1", refused, true, domain.CodePackageManaged},
	}
	for _, c := range cases {
		updater := &fakeUpdater{latest: c.latest, targetErr: c.targetErr}
		plan, err := New(nil, nil, nil, updater).CheckUpdate(t.Context(), c.current)
		var known *domain.Error
		if errors.As(err, &known) != (c.code != "") || (known != nil && known.Code != c.code) {
			t.Errorf("%s: %v", c.name, err)
		}
		if err == nil && (plan.IsNewer != c.newer || updater.targetUsed != c.newer) {
			t.Errorf("%s: %+v, target checked %v", c.name, plan, updater.targetUsed)
		}
	}
}

// TestUpdateInstallsThePlan passes the checked version and path on.
func TestUpdateInstallsThePlan(t *testing.T) {
	updater := &fakeUpdater{latest: "0.8.1"}
	portly := New(nil, nil, nil, updater)
	plan, err := portly.CheckUpdate(t.Context(), "0.8.0")
	if err == nil {
		err = portly.Update(t.Context(), plan)
	}
	if err != nil || updater.installed != "0.8.1 /home/dev/.local/bin/portly" {
		t.Errorf("%v %q", err, updater.installed)
	}
	offline := &fakeUpdater{latestErr: domain.NewError(domain.CodeUpdateUnavailable, "offline", "")}
	if _, err := New(nil, nil, nil, offline).CheckUpdate(t.Context(), "0.8.0"); err == nil || offline.targetUsed {
		t.Errorf("offline: %v", err)
	}
}
