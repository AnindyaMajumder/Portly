// Package arch enforces the modular monolith's boundaries.
package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"slices"
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
	"internal/modules/updater":   {"internal/domain"},
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
	"SendSignal": true, "SendSignalWithContext": true, "KillWithContext": true, "TerminateWithContext": true,
	"SuspendWithContext": true, "ResumeWithContext": true, "StartProcess": true, "NtTerminateProcess": true, "PtraceAttach": true,
}

// Calls that end a process without asking, only in ForceStop
var killingNames = map[string]bool{"SIGKILL": true, "TerminateProcess": true}

// Raw system calls, only where the terminal size is read
var rawCalls = map[string]bool{"Syscall": true, "Syscall6": true, "RawSyscall": true, "RawSyscall6": true, "SyscallN": true}

// Packages whose real name must stay visible at every call
var neverRenamed = []string{"os", "os/exec", "os/signal", "syscall", "golang.org/x/sys/unix", "golang.org/x/sys/windows"}

// Pure code reads no clock, file or network
var impureImports = []string{"os", "os/exec", "os/user", "time", "net", "net/http", "io/fs", "context", "syscall"}

var purePolicy = []string{"policy.go", "native.go", "container.go", "evidence.go", "names.go", "planid.go"}

// source is one Go file, path relative to the repository.
type source struct {
	path string
	text string
}

// layer is the package directory.
func (s source) layer() string { return path.Dir(s.path) }

// isTest covers test files and the tests directory.
func (s source) isTest() bool {
	return strings.HasPrefix(s.path, "tests/") || strings.HasSuffix(s.path, "_test.go")
}

// isExecutor covers the only files that may signal.
func (s source) isExecutor() bool {
	return s.layer() == "internal/modules/stopper" && strings.HasPrefix(path.Base(s.path), "exec_")
}

// check returns every rule a file breaks.
func check(s source) []string {
	problems := checkComments(s)
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, s.path, s.text, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return append(problems, err.Error())
	}
	problems = append(problems, checkFunctionComments(s, files, file)...)
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
		case *ast.FuncDecl:
			checkKills(s, node, report)
		case *ast.SelectorExpr:
			checkSelector(s, node, report)
		case *ast.BasicLit:
			checkLiteral(s, node, report)
		}
		return true
	})
	return problems
}

// checkSelector finds calls that belong to another layer.
func checkSelector(s source, node *ast.SelectorExpr, report func(ast.Node, string)) {
	owner, _ := node.X.(*ast.Ident)
	name := node.Sel.Name
	switch {
	case owner != nil && owner.Name == "domain" && name == "Approve" && s.layer() != "internal/modules/stopper":
		report(node, "only stopper may approve a target")
	case actingCalls[name] && !s.isExecutor() && !isSignalType(owner, name):
		report(node, name+" is only allowed in stopper executors")
	case name == "Terminate" && s.layer() != "internal/modules/stopper":
		report(node, "only stopper may terminate")
	case rawCalls[name] && !strings.HasPrefix(s.path, "internal/cli/terminal_"):
		report(node, "raw system calls are not allowed")
	case owner != nil && owner.Name == "exec" && name != "CommandContext" && name != "ExitError":
		report(node, "use exec.CommandContext with a deadline")
	case strings.HasPrefix(name, "Environ") && (owner == nil || owner.Name != "os"):
		report(node, "other processes' environment is never read")
	case owner != nil && owner.Name == "runtime" && (name == "GOOS" || name == "GOARCH"):
		report(node, "OS code belongs in suffixed files")
	case strings.HasPrefix(s.path, "internal/modules/") && isConsoleUse(owner, name):
		report(node, "modules do not print or exit")
	}
}

// isSignalType allows the os.Signal type, which sends nothing.
func isSignalType(owner *ast.Ident, name string) bool {
	return owner != nil && owner.Name == "os" && name == "Signal"
}

// isConsoleUse matches printing to the terminal and ending the process.
func isConsoleUse(owner *ast.Ident, name string) bool {
	if owner == nil {
		return false
	}
	isPrint := owner.Name == "fmt" && slices.Contains([]string{"Print", "Printf", "Println"}, name)
	return isPrint || owner.Name == "os" && slices.Contains([]string{"Exit", "Stdout", "Stderr", "Stdin"}, name) || owner.Name == "log"
}

// checkKills confines killing to ForceStop, which needs a forced approval.
func checkKills(s source, function *ast.FuncDecl, report func(ast.Node, string)) {
	if s.isExecutor() && function.Name.Name == "ForceStop" {
		return
	}
	ast.Inspect(function, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok && killingNames[selector.Sel.Name] {
			report(node, selector.Sel.Name+" is only allowed in ForceStop")
		}
		return true
	})
}

var engineAction = regexp.MustCompile(`(^|/)(stop|kill|restart|pause|unpause|remove|exec)\b`)

// checkLiteral finds forbidden paths and programs.
func checkLiteral(s source, node *ast.BasicLit, report func(ast.Node, string)) {
	text, err := strconv.Unquote(node.Value)
	if node.Kind != token.STRING || err != nil {
		return
	}
	isEngineAction := s.layer() == "internal/modules/docker" && engineAction.MatchString(text)
	isApproved := s.path == "internal/modules/docker/stop.go" && (text == "/stop?t=-1" || text == "/kill?signal=SIGKILL")
	switch {
	case isEngineAction && !isApproved:
		report(node, "engine actions belong in docker/stop.go, and stop only with t=-1")
	case text == "environ" || strings.Contains(text, "/environ") || strings.Contains(text, "procargs"):
		report(node, "other processes' environment is never read")
	case text == "sudo" || strings.HasSuffix(text, "/sudo"):
		report(node, "Portly never runs sudo")
	}
}

// checkImports enforces direction and library ownership.
func checkImports(s source, file *ast.File) []string {
	allowed, known := allowedImports[s.layer()]
	if !known {
		return []string{fmt.Sprintf("%s: package %s has no architecture rule", s.path, s.layer())}
	}
	var problems []string
	for _, spec := range file.Imports {
		name, _ := strconv.Unquote(spec.Path.Value)
		if problem := importProblem(s, name, spec.Name != nil); problem != "" {
			problems = append(problems, s.path+": "+problem)
		}
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

// importProblem covers cgo, running programs, renaming and pure code.
func importProblem(s source, name string, isRenamed bool) string {
	isPure := s.layer() == "internal/domain" || s.layer() == "internal/modules/stopper" && slices.Contains(purePolicy, path.Base(s.path))
	switch {
	case name == "C":
		return "cgo is not allowed"
	case name == "os/exec" && !s.isTest() && s.layer() != "internal/modules/discovery":
		return "only discovery may run programs"
	case isRenamed && slices.Contains(neverRenamed, name):
		return "do not rename " + name
	case isPure && !s.isTest() && slices.Contains(impureImports, name):
		return "pure code must not import " + name
	case strings.HasPrefix(name, "golang.org/x/sys/") && !s.isTest() && !hasOSSuffix(s.path):
		return "OS code belongs in suffixed files"
	}
	return ""
}

// hasOSSuffix matches files only one system compiles.
func hasOSSuffix(path string) bool {
	return slices.ContainsFunc([]string{"_linux.go", "_darwin.go", "_windows.go", "_unix.go"}, func(suffix string) bool {
		return strings.HasSuffix(path, suffix)
	})
}

// libraryProblem explains why a library is not allowed, or is empty.
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

// checkComments enforces the two-line limit.
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

// checkFunctionComments requires every function to say what it does.
func checkFunctionComments(s source, files *token.FileSet, file *ast.File) []string {
	var problems []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && (function.Doc == nil || strings.TrimSpace(function.Doc.Text()) == "") {
			problems = append(problems, fmt.Sprintf("%s: %s needs a comment", files.Position(function.Pos()), function.Name.Name))
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
