package output

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func publishFile(part, dest string, overwrite bool) error {
	from, err := windows.UTF16PtrFromString(part)
	if err != nil {
		return fmt.Errorf("cannot finalize output: %w", err)
	}
	to, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		return fmt.Errorf("cannot finalize output: %w", err)
	}
	var flags uint32
	if overwrite {
		flags = windows.MOVEFILE_REPLACE_EXISTING | windows.MOVEFILE_WRITE_THROUGH
	}
	// Go os.Rename replaces on Windows; MoveFileEx with 0 fails if dest already exists.
	if err := windows.MoveFileEx(from, to, flags); err != nil {
		if !overwrite && (errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, os.ErrExist)) {
			return fmt.Errorf("destination appeared during encoding: %s; pass --overwrite to replace it", dest)
		}
		return fmt.Errorf("cannot finalize output: %w", err)
	}
	return nil
}
