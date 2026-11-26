package app

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Updater finds and installs releases.
type Updater interface {
	Latest(ctx context.Context) (string, error)
	Target() (string, error)
	Install(ctx context.Context, version, path string) error
}

// UpdatePlan says what an update would do.
type UpdatePlan struct {
	Current string
	Latest  string
	// Where the new binary goes, set only when it is newer
	Path    string
	IsNewer bool
}

// Only release builds carry a plain version
var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

const installCommand = "curl -fsSL https://github.com/AnindyaMajumder/Portly/releases/latest/download/install.sh | sh"

// CheckUpdate compares this build with the latest release and checks it may be replaced.
func (a *App) CheckUpdate(ctx context.Context, current string) (UpdatePlan, error) {
	if !releaseVersion.MatchString(current) {
		return UpdatePlan{}, domain.NewError(domain.CodeDevBuild, "This is a development build ("+current+"), it can't update itself",
			"Install a release with: "+installCommand)
	}
	latest, err := a.updater.Latest(ctx)
	if err != nil {
		return UpdatePlan{}, err
	}
	plan := UpdatePlan{Current: current, Latest: latest, IsNewer: isNewer(latest, current)}
	if !plan.IsNewer {
		return plan, nil
	}
	plan.Path, err = a.updater.Target()
	return plan, err
}

// Update installs the planned release.
func (a *App) Update(ctx context.Context, plan UpdatePlan) error {
	return a.updater.Install(ctx, plan.Latest, plan.Path)
}

// isNewer compares two checked X.Y.Z versions.
func isNewer(candidate, current string) bool {
	next, now := strings.Split(candidate, "."), strings.Split(current, ".")
	for i := range next {
		a, _ := strconv.Atoi(next[i])
		b, _ := strconv.Atoi(now[i])
		if a != b {
			return a > b
		}
	}
	return false
}
