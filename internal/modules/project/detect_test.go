package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// tree creates files under a temporary home and returns it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// process builds a process of one user with a working directory.
func process(home, dir string, args ...string) domain.Process {
	found := domain.Process{Dir: dir, Home: home, Args: args, Name: "app"}
	found.UserID, found.Exe = "1000", "/usr/bin/app"
	return found
}

// TestWorkingDirectory is the strongest evidence, nearest manifest first.
func TestWorkingDirectory(t *testing.T) {
	home := tree(t, map[string]string{
		"code/web/package.json":                `{"name":"shop-web"}`,
		"code/web/src/a.js":                    "",
		"code/web/api/pyproject.toml":          "[project]\nname = \"shop-api\"\n",
		"code/web/node_modules/x/package.json": `{"name":"dependency"}`,
		"code/plain/.git/HEAD":                 "",
		"code/broken/package.json":             "{",
	})
	cases := map[string]string{
		"code/web": "shop-web", "code/web/src": "shop-web", "code/web/api": "shop-api",
		"code/web/node_modules/x": "shop-web", "code/plain": "plain", "code/broken": "broken",
	}
	for dir, want := range cases {
		got := New().Detect(t.Context(), process(home, filepath.Join(home, dir)), nil)
		if got == nil || got.Name != want || got.Confidence != domain.High {
			t.Errorf("%s: %+v, want %s", dir, got, want)
		}
	}
}

// TestNeverAProject covers home, above it, the root and missing directories.
func TestNeverAProject(t *testing.T) {
	home := tree(t, map[string]string{"package.json": `{"name":"dotfiles"}`, "go/pkg/mod/x@v1/go.mod": "module x\n", ".cache/y/package.json": "{}"})
	dirs := []string{home, filepath.Dir(home), "/", "", "relative", home + "/gone", home + "/code (deleted)", home + "/go/pkg/mod/x@v1", home + "/.cache/y", "/home/someone", "/root"}
	for _, dir := range dirs {
		if got := New().Detect(t.Context(), process(home, dir), nil); got != nil {
			t.Errorf("%q: %+v", dir, got)
		}
	}
}

// TestSymlinkedHome stops at the resolved home too.
func TestSymlinkedHome(t *testing.T) {
	real := tree(t, map[string]string{"package.json": "{}"})
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	if got := New().Detect(t.Context(), process(link, real), nil); got != nil {
		t.Errorf("got %+v", got)
	}
}

// TestArgumentsAndExecutable are medium evidence.
func TestArgumentsAndExecutable(t *testing.T) {
	home := tree(t, map[string]string{"code/api/pyproject.toml": "", "code/api/app/main.py": "", "code/api/.venv/pyvenv.cfg": "", "code/api/.venv/bin/python": ""})
	root := filepath.Join(home, "code/api")
	arguments := map[string][]string{
		"absolute": {"python", root + "/app/main.py"},
		"relative": {"python", "code/api/app/main.py"},
		"flag":     {"app", "--config=" + root + "/app/main.py"},
	}
	for name, args := range arguments {
		got := New().Detect(t.Context(), process(home, home, args...), nil)
		if got == nil || got.Root != root || got.Confidence != domain.Medium || !strings.HasPrefix(got.Evidence, "startup argument") {
			t.Errorf("%s: %+v", name, got)
		}
	}
	fromVenv := process(home, home)
	fromVenv.Exe = root + "/.venv/bin/python"
	if got := New().Detect(t.Context(), fromVenv, nil); got == nil || got.Root != root || !strings.HasPrefix(got.Evidence, "executable") {
		t.Errorf("venv: %+v", got)
	}
	for name, args := range map[string][]string{
		"url": {"curl", "http://host" + root + "/app/main.py"}, "flag only": {"app", "-x"}, "word": {"app", "serve"}, "program": {root + "/app/main.py"},
	} {
		if got := New().Detect(t.Context(), process(home, home, args...), nil); got != nil {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if got := argumentPaths(process(home, "", "app", "a/b.py")); len(got) != 0 {
		t.Errorf("relative path without a directory: %v", got)
	}
}

// TestAncestors count only for package runners of the same user.
func TestAncestors(t *testing.T) {
	home := tree(t, map[string]string{"code/web/package.json": `{"name":"web"}`})
	dir := filepath.Join(home, "code/web")
	runner := func(name string, args ...string) domain.Process {
		found := process(home, dir, args...)
		found.Name = name
		return found
	}
	npm := runner("npm run dev")
	otherUser, inContainer := npm, npm
	otherUser.UserID = "0"
	inContainer.ContainerIDs = []string{"x"}
	cases := []struct {
		name   string
		parent domain.Process
		want   bool
	}{
		{"npm title", npm, true},
		{"npm script", runner("node", "node", "/usr/lib/node_modules/npm/bin/npm-cli.js", "run"), true},
		{"make", runner("make"), true},
		{"shell", runner("bash"), false},
		{"tmux", runner("tmux: server"), false},
		{"editor", runner("code"), false},
		{"other user", otherUser, false},
		{"container", inContainer, false},
	}
	for _, c := range cases {
		got := New().Detect(t.Context(), process(home, home), []domain.Process{c.parent})
		if (got != nil) != c.want || got != nil && got.Confidence != domain.Medium {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

// TestContainersAndDeadlines return unknown instead of guessing.
func TestContainersAndDeadlines(t *testing.T) {
	home := tree(t, map[string]string{"code/package.json": "{}"})
	inside := process(home, filepath.Join(home, "code"))
	inside.ContainerIDs = []string{"x"}
	if got := New().Detect(t.Context(), inside, nil); got != nil {
		t.Errorf("container: %+v", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := New().Detect(ctx, process(home, filepath.Join(home, "code")), nil); got != nil {
		t.Errorf("expired: %+v", got)
	}
}
