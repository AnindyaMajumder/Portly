//go:build unix

package updater

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// TestPackagedPaths leaves package-managed binaries to their package manager.
func TestPackagedPaths(t *testing.T) {
	for path, want := range map[string]bool{
		"/usr/bin/portly": true, "/usr/sbin/portly": true, "/opt/homebrew/Cellar/portly/0.8.0/bin/portly": true,
		"/nix/store/abc-portly/bin/portly": true, "/usr/local/bin/portly": false, "/home/dev/.local/bin/portly": false,
	} {
		if isPackaged(path) != want {
			t.Errorf("%s: want %v", path, want)
		}
	}
}

// TestReadOnlyFolderIsAPermissionProblem points to the owner, which is the fix.
func TestReadOnlyFolderIsAPermissionProblem(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	path := installed(t)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	updater := &Updater{locate: func() (string, error) { return path, nil }}
	if _, err := updater.InstallPath(); codeOf(err) != domain.CodePermissionDenied {
		t.Errorf("got %v", err)
	}
}

// TestReplaceKeepsTheMode leaves custom permissions as they were.
func TestReplaceKeepsTheMode(t *testing.T) {
	path := installed(t)
	if err := os.Chmod(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := replace(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o750 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
}
