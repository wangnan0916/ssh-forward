package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTextFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestLoadConfigDefaultHost(t *testing.T) {
	path := writePolicyFile(t, `{"schema_version": 1, "default_host": "development"}`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if config.DefaultHost != "development" {
		t.Fatalf("DefaultHost = %q, want development", config.DefaultHost)
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	path := writePolicyFile(t, `{"schema_version": 1, "default_host": "development", "mystery": true}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("LoadConfig err = %v, want an unknown-field rejection", err)
	}
}

func TestLoadConfigRejectsUnsupportedSchema(t *testing.T) {
	path := writePolicyFile(t, `{"schema_version": 2, "default_host": "development"}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("LoadConfig err = %v, want a schema rejection", err)
	}
}

func TestLoadConfigRejectsBadJSONC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := writeTextFile(path, `{"schema_version": 1, "default_host": "dev",`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig on truncated JSONC succeeded")
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "absent.jsonc")); err == nil {
		t.Fatal("LoadConfig on a missing file succeeded")
	}
}

func TestSetDefaultHostWritesAndLoadsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := SetDefaultHost(path, "ubuntu"); err != nil {
		t.Fatalf("SetDefaultHost: %v", err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if config.DefaultHost != "ubuntu" {
		t.Fatalf("DefaultHost = %q, want ubuntu", config.DefaultHost)
	}
	// Overwriting is atomic and idempotent.
	if err := SetDefaultHost(path, "devbox"); err != nil {
		t.Fatalf("second SetDefaultHost: %v", err)
	}
	config, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig after overwrite: %v", err)
	}
	if config.DefaultHost != "devbox" {
		t.Fatalf("DefaultHost = %q, want devbox", config.DefaultHost)
	}
}
