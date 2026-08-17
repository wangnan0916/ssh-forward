package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// configSchemaVersion is the config.jsonc schema version (ADR-0005's
// independent-versioning convention, shared with policies.jsonc).
const configSchemaVersion = 1

// configFile is the config.jsonc shape (cli-and-state.md, Persistent
// intent): versioned and strict, like the policy file. Only the default
// host is read today; host lists, product settings, and the revisioned
// write path land with the desktop slice.
type configFile struct {
	SchemaVersion int    `json:"schema_version"`
	DefaultHost   string `json:"default_host,omitempty"`
}

// LoadConfig reads and validates config.jsonc from path. Invalid input
// (bad JSONC, unknown fields, unsupported schema) is rejected wholesale:
// a corrupt configuration must not silently drop the default host.
func LoadConfig(path string) (configFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return configFile{}, err
	}
	plain, err := stripJSONC(content)
	if err != nil {
		return configFile{}, err
	}
	var config configFile
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return configFile{}, fmt.Errorf("config.jsonc: %w", err)
	}
	if config.SchemaVersion != configSchemaVersion {
		return configFile{}, fmt.Errorf("config.jsonc: unsupported schema_version %d (want %d)", config.SchemaVersion, configSchemaVersion)
	}
	return config, nil
}

// SetDefaultHost writes config.jsonc with the given default host,
// replacing the file atomically. The schema today has exactly two fields;
// future fields land with the desktop's configuration surface.
func SetDefaultHost(path, host string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	config := configFile{SchemaVersion: configSchemaVersion, DefaultHost: host}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
