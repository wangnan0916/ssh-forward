package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTextFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := writeTextFile(path, content); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaultHost(t *testing.T) {
	path := writeConfigFile(t, `{"schema_version": 1, "default_host": "development"}`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if config.DefaultHost != "development" {
		t.Fatalf("DefaultHost = %q, want development", config.DefaultHost)
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	path := writeConfigFile(t, `{"schema_version": 1, "default_host": "development", "mystery": true}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("LoadConfig err = %v, want an unknown-field rejection", err)
	}
}

func TestLoadConfigRejectsUnsupportedSchema(t *testing.T) {
	path := writeConfigFile(t, `{"schema_version": 2, "default_host": "development"}`)
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

func TestPinnedHost(t *testing.T) {
	path := writeConfigFile(t, `{"schema_version": 1, "default_host": "ubuntu"}`)
	host, err := PinnedHost(path)
	if err != nil || host != "ubuntu" {
		t.Fatalf("PinnedHost = %q, %v; want ubuntu", host, err)
	}
	if _, err := PinnedHost(filepath.Join(t.TempDir(), "absent.jsonc")); !errors.Is(err, ErrNoHost) {
		t.Fatalf("missing file err = %v, want ErrNoHost", err)
	}
	empty := writeConfigFile(t, `{"schema_version": 1}`)
	if _, err := PinnedHost(empty); !errors.Is(err, ErrNoHost) {
		t.Fatalf("empty default err = %v, want ErrNoHost", err)
	}
}

func TestPortMutationsPreserveHostAndOtherPorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := SetDefaultHost(path, "dev"); err != nil {
		t.Fatal(err)
	}
	for _, port := range []uint16{8080, 5173, 8080} {
		if _, err := AddPort(path, "dev", port); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AddPort(path, "other", 3000); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemovePort(path, "dev", 5173); err != nil || !removed {
		t.Fatalf("RemovePort = %v, %v", removed, err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.DefaultHost != "dev" || len(config.Forwards["dev"]) != 1 || config.Forwards["dev"][0] != 8080 || config.Forwards["other"][0] != 3000 {
		t.Fatalf("config = %#v", config)
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
