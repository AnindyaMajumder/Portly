//go:build unix

package updater

import "testing"

// TestPackagedFolders leaves package-managed binaries to their package manager.
func TestPackagedFolders(t *testing.T) {
	for dir, want := range map[string]bool{"/usr/bin": true, "/usr/sbin": true, "/usr/local/bin": false, "/home/dev/.local/bin": false} {
		if isPackaged(dir) != want {
			t.Errorf("%s: want %v", dir, want)
		}
	}
}
