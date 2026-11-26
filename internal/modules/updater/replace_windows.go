package updater

import "os"

// isPackaged is false: Windows releases come only as archives.
func isPackaged(string) bool { return false }

// swap moves the running binary aside, which Windows allows, then puts the new one in its place.
func swap(next, path string) error {
	old := path + ".old"
	// Left by the previous update, whose process has ended
	_ = os.Remove(old)
	if err := os.Rename(path, old); err != nil {
		return err
	}
	if err := os.Rename(next, path); err != nil {
		_ = os.Rename(old, path)
		return err
	}
	return nil
}
