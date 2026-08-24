package cli

import (
	"io"
	"os"

	"golang.org/x/term"

	"github.com/wangnan0916/ssh-forward/cli/internal/statusview"
)

func statusViewOptions(writer io.Writer) statusview.Options {
	file, ok := writer.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return statusview.Options{}
	}
	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil {
		return statusview.Options{}
	}
	_, noColor := os.LookupEnv("NO_COLOR")
	ansiEnabled := !noColor
	return statusview.Options{Width: width, Color: ansiEnabled, Hyperlinks: ansiEnabled}
}
