//go:build unix

package updater

import (
	"os"
	"slices"
)

// Folders that only package managers write to
var packagedFolders = []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin"}

// isPackaged reports a binary a package manager owns.
func isPackaged(dir string) bool { return slices.Contains(packagedFolders, dir) }

// swap renames the new binary over the old one, which is atomic on Unix.
func swap(next, path string) error { return os.Rename(next, path) }
