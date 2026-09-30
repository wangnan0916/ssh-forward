package openssh

import (
	"io"
	"strings"

	_ "embed"
)

//go:embed scanner.sh
var scannerScript string

const scannerScriptEnd = "# ssh-forward-scanner-end"

// scannerBootstrap writes the embedded script to a temp file until scannerScriptEnd,
// then runs that file. The script keeps stdin and uses the following lines as probe requests.
const scannerBootstrap = `sh -c 'f=$(mktemp) || exit 1; trap "rm -f \"\$f\"" EXIT; while IFS= read -r l; do [ "$l" = "# ssh-forward-scanner-end" ] && break; printf "%s\n" "$l" >> "$f"; done; sh "$f"'`

func writeScannerScript(dst io.Writer) error {
	_, err := io.WriteString(dst, strings.TrimRight(scannerScript, "\n")+"\n"+scannerScriptEnd+"\n")
	return err
}
