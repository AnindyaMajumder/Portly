package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/app"
	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// stop plans, shows, asks, executes and reports.
func (s *session) stop(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return domain.NewError(domain.CodeUsage, "stop needs a port", "For example: portly stop 8000")
	}
	refs, err := parsePorts(args)
	if err != nil {
		return err
	}
	request := domain.StopRequest{Force: s.options.force}
	for _, ref := range refs {
		request.Endpoints = append(request.Endpoints, ref.StopEndpoint())
	}
	plan, err := s.app.Plan(ctx, request, s.options.expect)
	if err != nil {
		return err
	}
	s.diagnose(plan.Warnings, plan.Notes)
	// JSON never prompts, so without --yes it is a dry run
	canAsk := s.console.Interactive && !s.options.json
	if len(plan.Targets) > 0 && !s.options.yes && !canAsk {
		return s.reportUnconfirmed(plan)
	}
	var outcomes []domain.Outcome
	if len(plan.Targets) > 0 {
		if !s.options.json {
			s.showPlan(plan)
			fmt.Fprintln(s.console.Err, s.paintErr(yellow, irreversible))
		}
		if !s.options.yes && !s.ask(ctx, question(plan)) {
			return domain.NewError(domain.CodeCancelled, "Cancelled", "Nothing was stopped")
		}
		outcomes = s.app.Execute(ctx, plan, s.confirmForce(plan))
	}
	return s.reportStop(plan, outcomes)
}

// Shown before every stop, also with --yes
const irreversible = "Warning: stopping can't be undone. Unsaved work in it can be lost."

// reportUnconfirmed shows the plan and stops nothing.
func (s *session) reportUnconfirmed(plan app.Plan) error {
	failure := domain.NewError(domain.CodeConfirmationRequired, "Confirmation is required, nothing was stopped",
		"Add --yes to stop without asking, or --json to get a plan_id for --expect")
	s.exit = failure.Code.ExitCode()
	if s.options.json {
		document := s.stopDocument(plan, nil)
		document.Error = jsonError(failure)
		return s.writeJSON(document)
	}
	s.showPlan(plan)
	return failure
}

// showPlan prints every target and refusal before the question.
func (s *session) showPlan(plan app.Plan) {
	out := s.console.Err
	if len(plan.Stop.Results) == 1 {
		s.showTarget(plan.Targets[0])
		return
	}
	for _, result := range plan.Stop.Results {
		if result.Target < 0 {
			fmt.Fprintf(out, "  %-9s %s\n", portText(result.Endpoint), s.paintErr(yellow, s.explain(result.Endpoint, result.Refusal).Message))
			continue
		}
		target := plan.Targets[result.Target]
		fmt.Fprintf(out, "  %-9s %-20s %s  %s\n", portText(result.Endpoint), projectName(target), targetName(target), s.directory(target))
	}
	for _, target := range plan.Targets {
		s.showEffects(targetName(target)+": ", target)
	}
}

// showTarget prints the only target in detail.
func (s *session) showTarget(target app.Target) {
	out := s.console.Err
	fmt.Fprintf(out, "%-*s%s\n", labelWidth, "Project", projectName(target))
	label := "Process"
	if target.Container != nil {
		label = "Container"
	}
	fmt.Fprintf(out, "%-*s%s\n", labelWidth, label, targetName(target))
	if directory := s.directory(target); directory != "" {
		fmt.Fprintf(out, "%-*s%s\n", labelWidth, "Directory", directory)
	}
	s.showEffects("", target)
}

// showEffects lists everything else the stop touches.
func (s *session) showEffects(prefix string, target app.Target) {
	var effects []string
	var others, children []string
	for _, other := range target.Stop.Other {
		others = append(others, other.String()+"/"+string(other.Protocol))
	}
	for _, child := range target.Children {
		children = append(children, fmt.Sprintf("%s (PID %d)", child.Name, child.PID))
	}
	if len(others) > 0 {
		effects = append(effects, "also closes "+strings.Join(others, ", "))
	}
	if members := len(target.Stop.Members); target.Stop.Kind == domain.KindFamily {
		effects = append(effects, fmt.Sprintf("%d processes share the port", members))
	}
	if len(children) > 0 {
		effects = append(effects, "child processes: "+strings.Join(children, ", "))
	}
	if len(target.Stop.SharesNet) > 0 {
		effects = append(effects, "containers sharing its network: "+strings.Join(target.Stop.SharesNet, ", "))
	}
	flags := []struct {
		isSet bool
		text  string
	}{
		{target.Stop.Suspended, "suspended, will be resumed to stop"},
		{target.Stop.Paused, "paused, will be resumed to stop"},
		{target.Stop.AutoRemove, "will be removed when it stops (--rm)"},
		{target.Stop.Reloader, "runs under a reloader, which may start it again"},
		{!target.Stop.GracefulAvailable, "has no graceful stop and will be killed, unsaved data can be lost"},
	}
	for _, flag := range flags {
		if flag.isSet {
			effects = append(effects, flag.text)
		}
	}
	for _, effect := range effects {
		fmt.Fprintln(s.console.Err, s.paintErr(yellow, "  ! "+clean(prefix+effect)))
	}
}

// projectName is the project, or Unknown.
func projectName(target app.Target) string {
	if target.Project == nil {
		return unknownProject
	}
	return clean(target.Project.Name)
}

// targetName is how people recognize a target.
func targetName(target app.Target) string {
	if target.Container != nil {
		name := clean(target.Container.Name)
		if target.Container.ComposeService != "" {
			name += " (" + clean(target.Container.ComposeProject) + " / " + clean(target.Container.ComposeService) + ")"
		}
		return name
	}
	if target.Process == nil {
		return "Unknown process"
	}
	return fmt.Sprintf("%s (PID %d)", clean(target.Process.Name), target.Process.PID)
}

// directory is where a target runs.
func (s *session) directory(target app.Target) string {
	if target.Container != nil {
		return s.short(target.Container.Directory)
	}
	if target.Process == nil {
		return ""
	}
	return s.short(target.Process.Dir)
}

// question fits one or several targets. Kills are listed above it.
func question(plan app.Plan) string {
	if len(plan.Stop.Results) > 1 {
		return fmt.Sprintf("Stop %d of %d?", countAllowed(plan), len(plan.Stop.Results))
	}
	verb, noun := "Stop", "process"
	if !plan.Targets[0].Stop.GracefulAvailable {
		verb = "Kill"
	}
	if plan.Targets[0].Container != nil {
		noun = "container"
	}
	return verb + " this " + noun + "?"
}

// countAllowed counts ports, not targets.
func countAllowed(plan app.Plan) int {
	count := 0
	for _, result := range plan.Stop.Results {
		if result.Target >= 0 {
			count++
		}
	}
	return count
}

// ask reads a plain y. Anything else, or Ctrl-C, means no.
func (s *session) ask(ctx context.Context, question string) bool {
	answer, isAnswered := s.read(ctx, question+" [y/N] ")
	return isAnswered && (answer == "y" || answer == "yes")
}

// read prints a prompt and returns one lower-case line.
func (s *session) read(ctx context.Context, prompt string) (string, bool) {
	fmt.Fprint(s.console.Err, prompt)
	answers := make(chan string, 1)
	go func() {
		line, _ := s.input.ReadString('\n')
		answers <- line
	}()
	select {
	case line := <-answers:
		return strings.ToLower(strings.TrimSpace(line)), true
	case <-ctx.Done():
		fmt.Fprintln(s.console.Err)
		return "", false
	}
}

// confirmForce asks again before a kill. Only --yes skips it.
func (s *session) confirmForce(plan app.Plan) app.Confirm {
	return func(ctx context.Context, stop domain.StopTarget) bool {
		if s.options.yes {
			return true
		}
		if !s.console.Interactive || s.options.json {
			return false
		}
		for i := range plan.Stop.Targets {
			if plan.Stop.Targets[i].Root == stop.Root && plan.Stop.Targets[i].Container == stop.Container {
				if stop.GracefulAvailable {
					fmt.Fprintf(s.console.Err, "%s did not stop.\n", targetName(plan.Targets[i]))
				} else {
					fmt.Fprintf(s.console.Err, "%s has no graceful way to stop.\n", targetName(plan.Targets[i]))
				}
				s.showTarget(plan.Targets[i])
			}
		}
		fmt.Fprintln(s.console.Err, s.paintErr(red, "Killing it can lose unsaved data."))
		// A y typed while waiting must not become a kill
		answer, isAnswered := s.read(ctx, "Type kill to force kill it: ")
		return isAnswered && answer == "kill"
	}
}

// reportStop prints one result per port and sets the exit status.
func (s *session) reportStop(plan app.Plan, outcomes []domain.Outcome) error {
	s.exit = exitFor(plan, outcomes)
	if s.options.json {
		return s.writeJSON(s.stopDocument(plan, outcomes))
	}
	for _, result := range plan.Stop.Results {
		s.printResult(plan, result, outcomes)
	}
	return nil
}

// printResult prints success on stdout and everything else on stderr.
func (s *session) printResult(plan app.Plan, result domain.PortResult, outcomes []domain.Outcome) {
	failure := s.failureOf(plan, result, outcomes)
	if failure == nil {
		target := plan.Targets[result.Target]
		verb := "Stopped"
		if outcomes[result.Target].Method == domain.Forced {
			verb = "Killed"
		}
		s.println(s.paint(green, fmt.Sprintf("%s %s. Port %s is free.", verb, targetName(target), portText(result.Endpoint))))
		return
	}
	indent, prefix := "", s.paintErr(red, "Error: ")
	if len(plan.Stop.Results) > 1 {
		indent, prefix = "  ", ""
	}
	fmt.Fprintln(s.console.Err, prefix+clean(failure.Message)+".")
	if failure.Hint != "" {
		fmt.Fprintln(s.console.Err, indent+clean(failure.Hint))
	}
}

// failureOf explains a port that was not freed, nil when it was.
func (s *session) failureOf(plan app.Plan, result domain.PortResult, outcomes []domain.Outcome) *domain.Error {
	if result.Target < 0 {
		return s.explain(result.Endpoint, result.Refusal)
	}
	if outcomes == nil {
		return nil
	}
	outcome := outcomes[result.Target]
	if outcome.Code == "" {
		return nil
	}
	return s.explainOutcome(result.Endpoint, targetName(plan.Targets[result.Target]), outcome)
}

// exitFor is the shared exit status, or 8 when ports differ.
func exitFor(plan app.Plan, outcomes []domain.Outcome) int {
	exit, isFirst := 0, true
	for _, result := range plan.Stop.Results {
		code := result.Refusal.Code
		if result.Target >= 0 {
			code = outcomes[result.Target].Code
		}
		current := 0
		if code != "" {
			current = code.ExitCode()
		}
		if !isFirst && current != exit {
			return domain.ExitMixed
		}
		exit, isFirst = current, false
	}
	return exit
}
