package updater

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// isPackaged is false: Windows releases come only as archives.
func isPackaged(string) bool { return false }

// swap moves the running binary aside, which Windows allows, then puts the new one in its place.
func swap(next, path string) error {
	old := asideName(path)
	if err := os.Rename(path, old); err != nil {
		return err
	}
	if err := renameRetrying(next, path); err != nil {
		if restoreErr := os.Rename(old, path); restoreErr != nil {
			return domain.NewError(domain.CodeUpdateUnavailable, "The update failed and the previous portly could not be put back",
				"Rename "+old+" to "+path+" to use it again")
		}
		return err
	}
	return nil
}

// asideName is a free name for the running binary. A portly still running from an older one keeps it locked.
func asideName(path string) string {
	for i := range 100 {
		name := path + ".old"
		if i > 0 {
			name += strconv.Itoa(i)
		}
		if err := os.Remove(name); err == nil || errors.Is(err, fs.ErrNotExist) {
			return name
		}
	}
	return path + ".old" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// renameRetrying waits briefly for a virus scanner that holds the fresh file.
func renameRetrying(from, to string) error {
	var err error
	for range 10 {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}
