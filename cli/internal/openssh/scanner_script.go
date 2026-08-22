package openssh

import (
	_ "embed"
)

//go:embed scanner.sh
var scannerScript string
