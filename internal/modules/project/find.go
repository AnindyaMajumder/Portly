package project

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Deeper trees are not walked
const maxDepth = 40

// Nearest directory with one of these is a project, best name source first
var markers = []string{
	"go.mod", "package.json", "pyproject.toml", "Cargo.toml", "composer.json", "deno.json", "pom.xml",
	"build.gradle", "build.gradle.kts", "Gemfile", "mix.exs", "pubspec.yaml", "setup.py", "requirements.txt", "manage.py",
	"CMakeLists.txt", ".git",
}

// Dependencies and toolchains, never the user's project
var skippedNames = []string{
	"node_modules", "site-packages", "dist-packages", "vendor", ".venv", "venv", ".tox", ".nox", "__pypackages__",
	".cache", ".cargo", ".rustup", ".npm", ".nvm", ".pnpm-store", ".yarn", ".pyenv", ".rbenv", ".asdf", ".gradle", ".m2",
	".deno", ".bun", ".volta", ".sdkman", ".local",
}

// finder searches upwards and stops at the owner's home.
type finder struct{ homes []string }

// newFinder resolves the home directory, which may be a symlink.
func newFinder(home string) finder {
	homes := []string{"/root"}
	if home != "" {
		homes = append(homes, filepath.Clean(home))
		if resolved, err := filepath.EvalSymlinks(home); err == nil {
			homes = append(homes, resolved)
		}
	}
	return finder{homes: homes}
}

// find walks up from a path. Empty evidence means the marker itself.
func (f finder) find(path string, confidence domain.Confidence, evidence string) *domain.Project {
	if !filepath.IsAbs(path) {
		return nil
	}
	path = filepath.Clean(path)
	for range maxDepth {
		if f.isBoundary(path) {
			return nil
		}
		if !isSkipped(path) {
			if marker := markerIn(path); marker != "" {
				return describe(path, marker, confidence, evidence)
			}
		}
		path = filepath.Dir(path)
	}
	return nil
}

// isBoundary is true for the root, any home, and everything above a home.
func (f finder) isBoundary(path string) bool {
	parent := filepath.Dir(path)
	if parent == path || parent == "/home" || parent == "/Users" || strings.EqualFold(filepath.Base(parent), "Users") && filepath.Dir(filepath.Dir(parent)) == filepath.Dir(parent) {
		return true
	}
	for _, home := range f.homes {
		if path == home || strings.HasPrefix(home, path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// isSkipped reports a path inside dependencies or a toolchain cache.
func isSkipped(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	isSkippedName := slices.ContainsFunc(parts, func(part string) bool { return slices.Contains(skippedNames, part) })
	return isSkippedName || strings.Contains(filepath.ToSlash(path)+"/", "/go/pkg/mod/")
}

// markerIn returns the first marker in a directory, without following links.
func markerIn(directory string) string {
	// A virtual environment is not a project
	if _, err := os.Lstat(filepath.Join(directory, "pyvenv.cfg")); err == nil {
		return ""
	}
	for _, marker := range markers {
		info, err := os.Lstat(filepath.Join(directory, marker))
		if err == nil && (marker == ".git" || info.Mode().IsRegular()) {
			return marker
		}
	}
	return ""
}

// describe names the project from its manifest, then its directory.
func describe(root, marker string, confidence domain.Confidence, evidence string) *domain.Project {
	if evidence == "" {
		evidence = marker
	} else {
		evidence += ", " + marker
	}
	name := manifestName(marker, readManifest(root, marker))
	if name == "" {
		name = filepath.Base(root)
	}
	return &domain.Project{Name: name, Root: root, Confidence: confidence, Evidence: evidence}
}
