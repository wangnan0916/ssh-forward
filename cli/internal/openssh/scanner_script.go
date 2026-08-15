package openssh

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

const scannerVersion = 1

//go:embed scanner.sh
var scannerScript string

var embeddedScannerChecksum = func() string {
	digest := sha256.Sum256([]byte(scannerScript))
	return hex.EncodeToString(digest[:])
}()
