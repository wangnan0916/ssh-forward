package app

import (
	"io"
	"os"

	"golang.org/x/term"
)

// IsTerminal reports whether the reader is an interactive terminal.
func IsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
