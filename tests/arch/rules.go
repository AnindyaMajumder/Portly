// Package arch enforces the modular monolith's boundaries.
package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
)

const module = "github.com/AnindyaMajumder/Portly/"

// Which Portly packages each layer may import
var allowedImports = map[string][]string{
	"internal/domain":            {},
	"internal/platform/textsafe": {},
	"internal/modules/discovery": {"internal/domain"},
	"internal/modules/project":   {"internal/domain"},
	"internal/modules/docker":    {"internal/domain"},
	"internal/modules/stopper":   {"internal/domain"},
	"internal/app":               {"internal/domain", "internal/modules/"},
	"internal/cli":               {"internal/domain", "internal/app", "internal/platform/"},
	"cmd/portly":                 {"internal/app", "internal/cli", "internal/modules/"},
}

// Third-party libraries and the only layers using them
var libraryOwners = map[string][]string{
	"github.com/spf13/":  {"internal/cli"},
	"charm.land/":        {"internal/cli"},
	"github.com/shirou/": {"internal/modules/discovery"},
	"golang.org/x/sys/":  {"internal/modules/discovery", "internal/modules/stopper", "internal/modules/docker"},
}

// Calls that act on a process, only in stopper executors
var actingCalls = map[string]bool{
	"Kill": true, "Signal": true, "PidfdOpen": true, "PidfdSendSignal": true,
	"TerminateProcess": true, "FindProcess": true, "Tgkill": true, "GenerateConsoleCtrlEvent": true,
}

// source is one Go file, path relative to the repository.
type source struct {
	path string
	text string
}

func (s source) layer() string { return path.Dir(s.path) }

func (s source) isTest() bool {
	return strings.HasPrefix(s.path, "tests/") || strings.HasSuffix(s.path, "_test.go")
}

func (s source) isExecutor() bool {
	return s.layer() == "internal/modules/stopper" && strings.HasPrefix(path.Base(s.path), "exec_")
}

// check returns every rule a file breaks.
func check(s source) []string {
	problems := checkComments(s)
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, s.path, s.text, parser.SkipObjectResolution)
	if err != nil {
		return append(problems, err.Error())
	}
	if strings.HasPrefix(s.path, "tests/") {
		return problems
	}
	problems = append(problems, checkImports(s, file)...)
	if s.isTest() {
		return problems
	}
	report := func(node ast.Node, text string) {
		problems = append(problems, fmt.Sprintf("%s: %s", files.Position(node.Pos()), text))
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.SelectorExpr:
			checkSelector(s, node, report)
		case *ast.BasicLit:
			checkLiteral(s, node, report)
		}
		return true
	})
	return problems
}

func checkSelector(s source, node *ast.SelectorExpr, report func(ast.Node, string)) {
	owner, _ := node.X.(*ast.Ident)
	name := node.Sel.Name
	switch {
	case owner != nil && owner.Name == "domain" && name == "Approve" && s.layer() != "internal/modules/stopper":
		report(node, "only stopper may approve a target")
	case actingCalls[name] && !s.isExecutor() && !isContainerKill(s, name) && !isOwnSignalSetup(owner):
		report(node, name+" is only allowed in stopper executors")
	case owner != nil && owner.Name == "exec" && name == "Command":
		report(node, "use exec.CommandContext with a deadline")
	case owner != nil && owner.Name == "exec" && name == "CommandContext" && s.layer() != "internal/modules/discovery":
		report(node, "only discovery may run programs")
	case name == "Environ" && (owner == nil || owner.Name != "os"):
		report(node, "other processes' environment is never read")
	}
}

// The container executor and its interface are called Kill too
func isContainerKill(s source, name string) bool {
	isStopLayer := s.path == "internal/modules/docker/stop.go" || s.path == "internal/modules/stopper/run.go"
	return name == "Kill" && isStopLayer
}

// os/signal and os.Signal only concern Portly itself
func isOwnSignalSetup(owner *ast.Ident) bool {
	return owner != nil && (owner.Name == "os" || owner.Name == "signal")
}

func checkLiteral(s source, node *ast.BasicLit, report func(ast.Node, string)) {
	text, err := strconv.Unquote(node.Value)
	if node.Kind != token.STRING || err != nil {
		return
	}
	isEngineAction := strings.Contains(text, "/stop") && strings.Contains(text, "?t=") || strings.HasPrefix(text, "/kill")
	switch {
	case isEngineAction && s.path != "internal/modules/docker/stop.go":
		report(node, "engine stop and kill belong in docker/stop.go")
	case text == "environ" || strings.Contains(text, "/environ") || strings.Contains(text, "procargs"):
		report(node, "other processes' environment is never read")
	case text == "/usr/bin/sudo" || text == "/bin/sudo":
		report(node, "Portly never runs sudo")
	}
}

func checkImports(s source, file *ast.File) []string {
	allowed, known := allowedImports[s.layer()]
	if !known {
		return []string{fmt.Sprintf("%s: package %s has no architecture rule", s.path, s.layer())}
	}
	var problems []string
	for _, spec := range file.Imports {
		name, _ := strconv.Unquote(spec.Path.Value)
		local, isLocal := strings.CutPrefix(name, module)
		if isLocal && !hasAnyPrefix(local, allowed) {
			problems = append(problems, fmt.Sprintf("%s: %s must not import %s", s.path, s.layer(), local))
		}
		if spec.Name != nil && spec.Name.Name == "." {
			problems = append(problems, s.path+": dot imports hide where a call goes")
		}
		if isLocal && spec.Name != nil {
			problems = append(problems, s.path+": do not rename Portly imports")
		}
		if problem := libraryProblem(s, name); !isLocal && problem != "" {
			problems = append(problems, s.path+": "+problem)
		}
	}
	return problems
}

func libraryProblem(s source, name string) string {
	if !strings.Contains(strings.SplitN(name, "/", 2)[0], ".") {
		return ""
	}
	for prefix, owners := range libraryOwners {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if !hasAnyPrefix(s.layer(), owners) {
			return s.layer() + " must not use " + name
		}
		isAllowedOS := strings.HasSuffix(s.path, "_darwin.go") || strings.HasSuffix(s.path, "_windows.go")
		if prefix == "github.com/shirou/" && !isAllowedOS {
			return "gopsutil is only allowed in _darwin and _windows files"
		}
		return ""
	}
	return "unapproved dependency " + name
}

func checkComments(s source) []string {
	var problems []string
	run := 0
	for i, line := range strings.Split(s.text, "\n") {
		text := strings.TrimSpace(line)
		if strings.HasPrefix(text, "/*") {
			problems = append(problems, fmt.Sprintf("%s:%d: use // comments", s.path, i+1))
		}
		if !strings.HasPrefix(text, "//") || strings.HasPrefix(text, "//go:") {
			run = 0
			continue
		}
		if run++; run == 3 {
			problems = append(problems, fmt.Sprintf("%s:%d: comment block over 2 lines", s.path, i+1))
		}
	}
	return problems
}

// hasAnyPrefix matches exactly, or below a prefix ending in a slash.
func hasAnyPrefix(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if name == prefix || strings.HasSuffix(prefix, "/") && strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
