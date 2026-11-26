package app

import (
	"context"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Updater finds and installs releases.
type Updater interface {
	Latest(ctx context.Context) (string, error)
	InstallPath() (string, error)
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

const installCommand = "curl -fsSL https://github.com/AnindyaMajumder/Portly/releases/latest/download/install.sh | sh"

// CheckUpdate compares this build with the latest release and checks it may be replaced.
func (a *App) CheckUpdate(ctx context.Context, current string) (UpdatePlan, error) {
	if !domain.IsReleaseVersion(current) {
		return UpdatePlan{}, domain.NewError(domain.CodeDevBuild, "This is a development build ("+current+"), it can't update itself",
			"Install a release with: "+installCommand)
	}
	latest, err := a.updater.Latest(ctx)
	if err != nil {
		return UpdatePlan{}, err
	}
	plan := UpdatePlan{Current: current, Latest: latest, IsNewer: domain.IsNewerVersion(latest, current)}
	if !plan.IsNewer {
		return plan, nil
	}
	plan.Path, err = a.updater.InstallPath()
	return plan, err
}

// Update installs the planned release, checking the plan again so no caller can downgrade.
func (a *App) Update(ctx context.Context, plan UpdatePlan) error {
	if !domain.IsNewerVersion(plan.Latest, plan.Current) || plan.Path == "" {
		return domain.NewError(domain.CodeUpdateUnavailable, "Nothing newer to install", "Run 'portly update' again")
	}
	return a.updater.Install(ctx, plan.Latest, plan.Path)
}
