package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/AnindyaMajumder/Portly/internal/app"
	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Update statuses in JSON
const (
	statusUpdated              = "updated"
	statusUpToDate             = "up_to_date"
	statusConfirmationRequired = "confirmation_required"
)

// updateCommand builds portly update.
func (s *session) updateCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "update",
		Short: "Update portly to the latest release",
		Long:  "Checks GitHub for a newer release, asks, then downloads it, verifies its checksum and replaces this binary.",
		Example: "  portly update             check, ask, then update\n  portly update --yes       do not ask\n" +
			"  portly update --json      check only, change nothing",
		Args: cobra.NoArgs, SilenceErrors: true, SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			s.command = "update"
			return s.update(command.Context())
		},
	}
	command.Flags().BoolVarP(&s.options.yes, "yes", "y", false, "do not ask")
	return command
}

// update checks for a newer release and installs it after asking.
func (s *session) update(ctx context.Context) error {
	plan, err := s.app.CheckUpdate(ctx, s.console.Version)
	if err != nil {
		return s.updateFailure(err)
	}
	if !plan.IsNewer {
		return s.reportUpdate(plan, statusUpToDate, nil)
	}
	// JSON never prompts, so without --yes it only checks
	if !s.options.yes && (s.options.json || !s.console.Interactive) {
		failure := domain.NewError(domain.CodeConfirmationRequired, "Confirmation is required, nothing was changed", "Add --yes to update without asking")
		return s.reportUpdate(plan, statusConfirmationRequired, failure)
	}
	if !s.options.yes && !s.ask(ctx, fmt.Sprintf("Update portly %s → %s?", plan.Current, plan.Latest)) {
		return domain.NewError(domain.CodeCancelled, "Cancelled", "Nothing was changed")
	}
	if err := s.app.Update(ctx, plan); err != nil {
		return s.updateFailure(err)
	}
	return s.reportUpdate(plan, statusUpdated, nil)
}

// reportUpdate prints the result, and the failure that comes with it.
func (s *session) reportUpdate(plan app.UpdatePlan, status string, failure *domain.Error) error {
	if s.options.json {
		document := newDocument("update", nil)
		document.Update = &updateJSON{Current: plan.Current, Latest: plan.Latest, Status: status}
		if failure != nil {
			document.Error, s.exit = jsonError(failure), failure.Code.ExitCode()
		}
		return s.writeJSON(document)
	}
	switch status {
	case statusUpToDate:
		s.println(fmt.Sprintf("portly %s is up to date.", plan.Current))
	case statusUpdated:
		s.println(fmt.Sprintf("Updated portly %s → %s in %s", plan.Current, plan.Latest, s.short(plan.Path)))
	case statusConfirmationRequired:
		s.println(fmt.Sprintf("portly %s is available, you have %s.", plan.Latest, plan.Current))
		return failure
	}
	return nil
}

// updateFailure says nothing changed on Ctrl-C, and how to update a folder the user can't write.
func (s *session) updateFailure(err error) error {
	var known *domain.Error
	switch {
	case errors.Is(err, context.Canceled):
		return domain.NewError(domain.CodeCancelled, "Cancelled", "Nothing was changed")
	case !errors.As(err, &known) || known.Code != domain.CodePermissionDenied:
		return err
	case s.console.Windows:
		return domain.NewError(known.Code, known.Message, "Run 'portly update' in an administrator terminal")
	}
	return domain.NewError(known.Code, known.Message, "To update as administrator:  sudo "+quote(s.console.Executable)+" update")
}
