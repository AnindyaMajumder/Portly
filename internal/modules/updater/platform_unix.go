//go:build unix

package updater

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Folders that only package managers write to
var packagedFolders = []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin"}

// Paths that Homebrew, Nix and snap manage, even when the user can write them
var packagedParts = []string{"/Cellar/", "/nix/store/", "/snap/"}

// isPackaged reports a binary a package manager owns.
func isPackaged(path string) bool {
	return slices.Contains(packagedFolders, filepath.Dir(path)) ||
		slices.ContainsFunc(packagedParts, func(part string) bool { return strings.Contains(path, part) })
}

// swap renames the new binary over the old one, which is atomic on Unix.
func swap(next, path string) error { return os.Rename(next, path) }
