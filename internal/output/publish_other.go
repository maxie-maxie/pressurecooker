//go:build !windows

package output

import (
	"fmt"
	"os"
)

func publishFile(part, dest string, overwrite bool) error {
	if overwrite {
		if err := os.Rename(part, dest); err != nil {
			return fmt.Errorf("cannot finalize output: %w", err)
		}
		return nil
	}
	// Linking refuses concurrent destination creation atomically on POSIX.
	return publishLinkNoOverwrite(part, dest, os.Link, os.Remove)
}
