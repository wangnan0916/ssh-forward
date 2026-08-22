package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/renameio/v2"
	"github.com/tailscale/hujson"
)

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return renameio.WriteFile(path, data, 0o600, renameio.WithStaticPermissions(0o600))
}

func readJSONC(path, label string, dest any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	plain, err := hujson.Standardize(content)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func writeJSONC(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return writeAtomic(path, encoded)
}

func checkSchemaVersion(label string, got, want int) error {
	if got != want {
		return fmt.Errorf("%s: unsupported schema_version %d (want %d)", label, got, want)
	}
	return nil
}
