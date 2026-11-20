//go:build !windows

package tui

import (
	"os"

	"golang.org/x/term"
)

func sessionVT(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }
