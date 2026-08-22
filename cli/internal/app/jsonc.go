package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tailscale/hujson"
)

func writeAtomic(path string, data []byte, tmpPattern string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), tmpPattern)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
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

func writeJSONC(path, tmpPattern string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return writeAtomic(path, encoded, tmpPattern)
}

func checkSchemaVersion(label string, got, want int) error {
	if got != want {
		return fmt.Errorf("%s: unsupported schema_version %d (want %d)", label, got, want)
	}
	return nil
}
