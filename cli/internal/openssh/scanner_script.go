package openssh

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// scannerVersion is the frame-protocol version, identical to the "SF1"
// prefix the script emits and the parser checks (scanner.go). The parser
// stamps it into every ObservationSet; bump it together with the prefix
// when the frame layout changes.
const scannerVersion = 1

//go:embed scanner.sh
var scannerScript string

var embeddedScannerChecksum = func() string {
	digest := sha256.Sum256([]byte(scannerScript))
	return hex.EncodeToString(digest[:])
}()
