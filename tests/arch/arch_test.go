package arch

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestRepositoryFollowsTheRules(t *testing.T) {
	root := os.DirFS("../..")
	checked := 0
	err := fs.WalkDir(root, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && path != "." && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "testdata") {
			return fs.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		text, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		checked++
		for _, problem := range check(source{path, string(text)}) {
			t.Error(problem)
		}
		return nil
	})
	if err != nil || checked < 5 {
		t.Fatalf("checked %d files: %v", checked, err)
	}
}

func TestRulesCatchViolations(t *testing.T) {
	importing := func(name string) string { return "package x\nimport _ \"" + name + "\"\n" }
	body := func(code string) string { return "package x\nfunc f() {\n" + code + "\n}\n" }
	cases := []struct{ name, path, text string }{
		{"module imports module", "internal/modules/docker/a.go", importing(module + "internal/modules/stopper")},
		{"domain imports app", "internal/domain/a.go", importing(module + "internal/app")},
		{"cli imports module", "internal/cli/a.go", importing(module + "internal/modules/stopper")},
		{"unknown package", "internal/utils/a.go", "package utils\n"},
		{"cobra outside cli", "internal/app/a.go", importing("github.com/spf13/cobra")},
		{"gopsutil on linux", "internal/modules/discovery/a_linux.go", importing("github.com/shirou/gopsutil/v4/process")},
		{"gopsutil in shared file", "internal/modules/discovery/a.go", importing("github.com/shirou/gopsutil/v4/process")},
		{"unapproved library", "internal/cli/a.go", importing("example.com/x")},
		{"long comment", "internal/domain/a.go", "package x\n// a\n// b\n// c\n"},
		{"block comment", "internal/domain/a.go", "package x\n/* a */\n"},
		{"signal in policy", "internal/modules/stopper/policy.go", body("unix.Kill(1, 9)")},
		{"signal in discovery", "internal/modules/discovery/a_linux.go", body("p.Signal(nil)")},
		{"pidfd outside executor", "internal/modules/discovery/a_linux.go", body("unix.PidfdOpen(1, 0)")},
		{"terminate outside executor", "internal/app/a.go", body("windows.TerminateProcess(1, 1)")},
		{"executor name elsewhere", "internal/app/exec_linux.go", body("unix.Kill(1, 9)")},
		{"approve in app", "internal/app/a.go", body("domain.Approve(a, b, c)")},
		{"approve in docker", "internal/modules/docker/stop.go", body("domain.Approve(a, b, c)")},
		{"engine stop elsewhere", "internal/modules/docker/list.go", body("get(\"/stop?t=-1\")")},
		{"environ path", "internal/modules/discovery/a_linux.go", body("read(\"environ\")")},
		{"gopsutil environ", "internal/modules/discovery/a_darwin.go", body("p.Environ()")},
		{"sudo", "internal/modules/discovery/a.go", body("run(\"/usr/bin/sudo\")")},
		{"no deadline", "internal/modules/discovery/a.go", body("exec.Command(\"/bin/ls\")")},
		{"program in app", "internal/app/a.go", body("exec.CommandContext(ctx, \"/bin/ls\")")},
		{"renamed import", "internal/app/a.go", "package x\nimport d \"" + module + "internal/domain\"\nvar _ d.Code\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if len(check(source{c.path, c.text})) == 0 {
				t.Error("violation was not caught")
			}
		})
	}
}

func TestRulesAllowTheDesign(t *testing.T) {
	body := func(code string) string { return "package x\nfunc f() {\n" + code + "\n}\n" }
	cases := []source{
		{"internal/modules/stopper/exec_linux.go", body("unix.PidfdSendSignal(1, 15, nil, 0)")},
		{"internal/modules/stopper/run.go", body("domain.Approve(a, b, c)")},
		{"internal/modules/docker/stop.go", body("post(\"/containers/x/stop?t=-1\")")},
		{"internal/cli/a.go", body("signal.NotifyContext(ctx, os.Interrupt)")},
		{"internal/modules/discovery/a_darwin.go", body("exec.CommandContext(ctx, \"/usr/sbin/lsof\")")},
		{"internal/domain/a.go", "package x\n// one\n// two\n"},
	}
	for _, c := range cases {
		if problems := check(c); len(problems) > 0 {
			t.Error(problems)
		}
	}
}
