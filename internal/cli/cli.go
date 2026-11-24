// Package cli turns arguments into use cases and results into output.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AnindyaMajumder/Portly/internal/app"
	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// UseCases is what the command line needs from the app.
type UseCases interface {
	List(ctx context.Context, all bool) (app.Report, error)
	Inspect(ctx context.Context, refs []domain.PortRef) (app.Report, error)
	Plan(ctx context.Context, request domain.StopRequest, expect string) (app.Plan, error)
	Execute(ctx context.Context, plan app.Plan, confirm app.Confirm) []domain.Outcome
}

// Console is everything outside the process, so tests can replace it.
type Console struct {
	In          io.Reader
	Out         io.Writer
	Err         io.Writer
	Interactive bool
	Color       bool
	// Zero when output is not a terminal
	Width      int
	Version    string
	Executable string
	Home       string
	Windows    bool
}

// options are the parsed flags.
type options struct {
	json    bool
	verbose bool
	all     bool
	yes     bool
	force   bool
	expect  string
}

// session is one invocation.
type session struct {
	console Console
	app     UseCases
	options options
	input   *bufio.Reader
	// Name used in JSON, unknown until a command runs
	command string
	exit    int
}

// Run executes one command line and returns the exit status.
func Run(ctx context.Context, args []string, console Console, useCases UseCases) (exit int) {
	s := &session{console: console, app: useCases, input: bufio.NewReader(console.In), command: "unknown"}
	// Also true when flags never get parsed
	s.options.json = slices.Contains(args, "--json")
	defer func() {
		if recovered := recover(); recovered != nil {
			exit = s.fail(fmt.Errorf("unexpected failure: %v", recovered))
		}
	}()
	root := s.rootCommand()
	root.SetArgs(stopFirst(args))
	if err := root.ExecuteContext(ctx); err != nil {
		return s.fail(err)
	}
	return s.exit
}

// stopFirst lets flags come before the stop command.
func stopFirst(args []string) []string {
	for i, arg := range args {
		if arg == "stop" {
			return slices.Concat([]string{"stop"}, args[:i], args[i+1:])
		}
		isValue := i > 0 && args[i-1] == "--expect"
		if !strings.HasPrefix(arg, "-") && !isValue {
			break
		}
	}
	// Cobra reads the real command line when given nil
	return append([]string{}, args...)
}

// rootCommand builds the command tree.
func (s *session) rootCommand() *cobra.Command {
	cobra.MousetrapHelpText = ""
	root := &cobra.Command{
		Use:   "portly [port ...]",
		Short: "See what uses your ports, and stop it safely",
		Long:  "Portly shows what uses local TCP and UDP ports, which project it belongs to, and stops it safely.",
		Example: "  portly                    list ports\n  portly 8000               details for a port\n" +
			"  portly 8000/udp           only UDP\n  portly stop 8000          stop it, after asking\n  portly --json             for scripts and agents",
		Args: cobra.ArbitraryArgs, Version: s.console.Version, SilenceErrors: true, SilenceUsage: true,
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 0 {
				return s.list(command.Context())
			}
			return s.inspect(command.Context(), args)
		},
	}
	root.SetIn(s.console.In)
	root.SetOut(s.console.Out)
	root.SetErr(s.console.Err)
	root.SetVersionTemplate("portly {{.Version}}\n")
	root.PersistentFlags().BoolVar(&s.options.json, "json", s.options.json, "print one JSON document")
	root.PersistentFlags().BoolVar(&s.options.verbose, "verbose", false, "print diagnostics on stderr")
	root.Flags().BoolVar(&s.options.all, "all", false, "also list likely-client UDP ports")
	root.SetFlagErrorFunc(func(command *cobra.Command, err error) error {
		return domain.NewError(domain.CodeUsage, err.Error(), "Run '"+command.CommandPath()+" --help' to see the options")
	})
	root.AddCommand(s.stopCommand())
	return root
}

// stopCommand builds portly stop.
func (s *session) stopCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "stop <port> ...",
		Short: "Stop what uses a port, after showing it and asking",
		Long:  "Shows exactly what would stop, asks, stops it gracefully and checks the port is free. A bare port means TCP.",
		Example: "  portly stop 8000                  ask, then stop\n  portly stop 3000 8000             several, one question\n" +
			"  portly stop 5353/udp              a UDP port\n  portly stop 8000 --yes            do not ask\n" +
			"  portly stop 8000 --force          graceful first, then ask again and kill\n" +
			"  portly stop 8000 --json           show the targets and a plan_id, stop nothing",
		Args: cobra.ArbitraryArgs, SilenceErrors: true, SilenceUsage: true,
		RunE: func(command *cobra.Command, args []string) error { return s.stop(command.Context(), args) },
	}
	command.Flags().BoolVarP(&s.options.yes, "yes", "y", false, "do not ask, every check still runs")
	command.Flags().BoolVar(&s.options.force, "force", false, "kill if a graceful stop fails, after asking again")
	command.Flags().StringVar(&s.options.expect, "expect", "", "stop only if the targets match this plan_id")
	return command
}

// list prints every port.
func (s *session) list(ctx context.Context) error {
	s.command = "list"
	report, err := s.app.List(ctx, s.options.all)
	if err != nil {
		return err
	}
	s.diagnose(report.Warnings, report.Notes)
	if s.options.json {
		return s.writeJSON(reportDocument("list", report))
	}
	s.renderList(report)
	return nil
}

// inspect prints named ports.
func (s *session) inspect(ctx context.Context, args []string) error {
	s.command = "inspect"
	refs, err := parsePorts(args)
	if err != nil {
		return err
	}
	if s.options.all {
		return domain.NewError(domain.CodeUsage, "--all only applies to the list", "Run 'portly --all', or name the port without it")
	}
	report, err := s.app.Inspect(ctx, refs)
	if err != nil {
		return err
	}
	s.diagnose(report.Warnings, report.Notes)
	if s.options.json {
		return s.writeJSON(reportDocument("inspect", report))
	}
	s.renderDetails(report)
	return nil
}

// Words people try instead of stop
var stopWords = []string{"kill", "killall", "close", "end", "quit", "free", "terminate"}

// parsePorts rejects the whole command when one port is invalid.
func parsePorts(args []string) ([]domain.PortRef, error) {
	if len(args) > 0 && slices.Contains(stopWords, strings.ToLower(args[0])) {
		suggestion := clean(strings.Join(append([]string{"portly", "stop"}, args[1:]...), " "))
		return nil, domain.NewError(domain.CodeUsage, fmt.Sprintf("Unknown command %q", clean(args[0])), "Did you mean '"+suggestion+"'?")
	}
	var refs []domain.PortRef
	for _, arg := range args {
		ref, err := domain.ParsePortRef(arg)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// diagnose prints warnings always and notes with --verbose.
func (s *session) diagnose(warnings, notes []string) {
	for _, warning := range warnings {
		fmt.Fprintln(s.console.Err, "Warning:", clean(warning))
	}
	if !s.options.verbose {
		return
	}
	for _, note := range notes {
		fmt.Fprintln(s.console.Err, "Note:", clean(note))
	}
}

// fail prints one error and returns its exit status.
func (s *session) fail(err error) int {
	failure := domain.AsError(err)
	if errors.Is(err, context.Canceled) {
		failure = domain.NewError(domain.CodeCancelled, "Cancelled", "Nothing was stopped")
	}
	if s.options.json {
		if writeErr := s.writeJSON(failureDocument(s.command, failure)); writeErr != nil {
			return domain.CodeInternal.ExitCode()
		}
		return failure.Code.ExitCode()
	}
	fmt.Fprintln(s.console.Err, s.paint(red, "Error: ")+clean(failure.Message))
	if failure.Hint != "" {
		fmt.Fprintln(s.console.Err, clean(failure.Hint))
	}
	return failure.Code.ExitCode()
}

// SystemConsole describes the real terminal.
func SystemConsole(version string) Console {
	executable, _ := os.Executable()
	home, _ := os.UserHomeDir()
	width, isTerminal := windowSize(os.Stdout)
	_, isInput := windowSize(os.Stdin)
	_, isError := windowSize(os.Stderr)
	return Console{
		In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Interactive: isInput && isError, Width: width,
		Color:   isTerminal && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
		Version: version, Executable: executable, Home: home, Windows: os.PathSeparator == '\\',
	}
}
