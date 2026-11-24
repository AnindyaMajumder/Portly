// Package project finds which source directory a process belongs to.
package project

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Arguments beyond this are data, not scripts
const maxArguments = 16

// Only these parents run programs on behalf of a project
var runners = []string{
	"npm", "npx", "pnpm", "yarn", "bun", "bunx", "deno", "go", "cargo", "make", "just", "task", "uv", "uvx",
	"poetry", "pipenv", "pdm", "hatch", "gradle", "gradlew", "mvn", "mvnw", "bundle", "rake", "composer", "mix", "dotnet",
}

// Detector reads manifests. The zero value works.
type Detector struct{}

// New returns a detector.
func New() *Detector { return &Detector{} }

// Detect returns the project with evidence, or nil. It never guesses.
func (*Detector) Detect(ctx context.Context, process domain.Process, ancestors []domain.Process) *domain.Project {
	// Paths of a container mean nothing on the host
	if len(process.ContainerIDs) > 0 || ctx.Err() != nil {
		return nil
	}
	found := make(chan *domain.Project, 1)
	go func() { found <- detect(process, ancestors) }()
	select {
	case project := <-found:
		return project
	case <-ctx.Done():
		// A dead network path must not block the rest
		return nil
	}
}

// detect tries evidence from strongest to weakest.
func detect(process domain.Process, ancestors []domain.Process) *domain.Project {
	finder := newFinder(process.Home)
	if project := finder.find(process.Dir, domain.High, ""); project != nil {
		return project
	}
	for _, path := range argumentPaths(process) {
		if project := finder.find(path, domain.Medium, "startup argument"); project != nil {
			return project
		}
	}
	if project := finder.find(filepath.Dir(process.Exe), domain.Medium, "executable"); project != nil {
		return project
	}
	for _, ancestor := range ancestors {
		runner := runnerName(ancestor)
		if runner == "" || ancestor.UserID == "" || ancestor.UserID != process.UserID || len(ancestor.ContainerIDs) > 0 {
			continue
		}
		if project := finder.find(ancestor.Dir, domain.Medium, "started by "+runner); project != nil {
			return project
		}
	}
	return nil
}

// argumentPaths lists arguments that could be files, made absolute.
func argumentPaths(process domain.Process) []string {
	var paths []string
	for i, argument := range process.Args {
		if i == 0 || i > maxArguments {
			continue
		}
		if _, value, hasValue := strings.Cut(argument, "="); hasValue {
			argument = value
		}
		looksLikePath := strings.ContainsRune(argument, filepath.Separator) || filepath.Ext(argument) != ""
		if argument == "" || strings.HasPrefix(argument, "-") || strings.Contains(argument, "://") || !looksLikePath {
			continue
		}
		if !filepath.IsAbs(argument) {
			if !filepath.IsAbs(process.Dir) {
				continue
			}
			argument = filepath.Join(process.Dir, argument)
		}
		paths = append(paths, filepath.Clean(argument))
	}
	return paths
}

// runnerName returns the package runner a process is, or nothing.
func runnerName(process domain.Process) string {
	names := []string{process.Name, filepath.Base(process.Exe)}
	for i, argument := range process.Args {
		if i > 1 {
			break
		}
		names = append(names, filepath.Base(argument))
	}
	for _, name := range names {
		first, _, _ := strings.Cut(name, " ")
		first = strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(first), ".exe"), "-cli.js")
		if slices.Contains(runners, first) {
			return first
		}
	}
	return ""
}
