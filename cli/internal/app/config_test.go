package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
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
	path := writeConfigFile(t, `{"schema_version": 5, "default_host": "development"}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("LoadConfig err = %v, want a schema rejection", err)
	}
}

func TestWorkingDirectoryRuleMutationsPreserveHostAndUpgradeSchema(t *testing.T) {
	path := writeConfigFile(t, `{"schema_version": 1, "default_host": "dev"}`)
	for _, pattern := range []string{"/workspace/**", "/workspace/apps/*", "/workspace/**"} {
		if _, err := AddWorkingDirectoryRule(path, "dev", pattern); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AddWorkingDirectoryRule(path, "other", "/srv/**"); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveWorkingDirectoryRule(path, "dev", "/workspace/apps/*"); err != nil || !removed {
		t.Fatalf("RemoveWorkingDirectoryRule = %v, %v", removed, err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.SchemaVersion != 4 || config.DefaultHost != "dev" ||
		len(config.WorkingDirectoryRules["dev"]) != 1 || config.WorkingDirectoryRules["dev"][0] != "/workspace/**" ||
		config.WorkingDirectoryRules["other"][0] != "/srv/**" {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadConfigRejectsInvalidWorkingDirectoryRules(t *testing.T) {
	for _, pattern := range []string{"workspace/**", "/workspace/["} {
		path := writeConfigFile(t, `{"schema_version": 2, "working_directory_rules": {"dev": [`+strconv.Quote(pattern)+`]}}`)
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "working-directory glob") {
			t.Fatalf("LoadConfig pattern %q err = %v", pattern, err)
		}
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

func TestForwardMutationsPreserveHostAndOtherForwards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := SetDefaultHost(path, "dev"); err != nil {
		t.Fatal(err)
	}
	for _, forward := range []core.RememberedForward{
		{RemotePort: 8080, LocalPort: 18080},
		{RemotePort: 5173, LocalPort: 5173},
		{RemotePort: 8080, LocalPort: 28080},
		{RemotePort: 8080, LocalPort: 28080},
	} {
		if _, err := SetRememberedForward(path, "dev", forward); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SetRememberedForward(path, "other", core.RememberedForward{
		RemotePort: 3000, LocalPort: 13000,
	}); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveRememberedForward(path, "dev", 5173); err != nil || !removed {
		t.Fatalf("RemoveRememberedForward = %v, %v", removed, err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.DefaultHost != "dev" ||
		len(config.RememberedForwards["dev"]) != 1 ||
		config.RememberedForwards["dev"][0] != (core.RememberedForward{RemotePort: 8080, LocalPort: 28080}) ||
		config.RememberedForwards["other"][0] != (core.RememberedForward{RemotePort: 3000, LocalPort: 13000}) {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadConfigMigratesLegacyPortsToSamePortForwards(t *testing.T) {
	path := writeConfigFile(t, `{"schema_version": 2, "forwards": {"dev": [5173, 3000]}}`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []core.RememberedForward{
		{RemotePort: 3000, LocalPort: 3000, AllowFallback: true},
		{RemotePort: 5173, LocalPort: 5173, AllowFallback: true},
	}
	if config.LegacyForwards != nil || len(config.RememberedForwards["dev"]) != len(want) {
		t.Fatalf("config = %#v", config)
	}
	for index := range want {
		if config.RememberedForwards["dev"][index] != want[index] {
			t.Fatalf("forwards = %#v, want %#v", config.RememberedForwards["dev"], want)
		}
	}
}

func TestLoadConfigMigratesSchemaThreePortPolicies(t *testing.T) {
	path := writeConfigFile(t, `{
		"schema_version": 3,
		"remembered_forwards": {"dev": [
			{"remote_port": 3000, "local_port": 3000},
			{"remote_port": 5173, "local_port": 15173, "allow_fallback": true}
		]}
	}`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []core.RememberedForward{
		{RemotePort: 3000, LocalPort: 3000, AllowFallback: true},
		{RemotePort: 5173, LocalPort: 15173},
	}
	if got := config.RememberedForwards["dev"]; !slices.Equal(got, want) {
		t.Fatalf("forwards = %#v, want %#v", got, want)
	}
}

func TestLoadConfigDefaultsOmittedLocalPortToFallback(t *testing.T) {
	path := writeConfigFile(t, `{
		"schema_version": 4,
		"remembered_forwards": {"dev": [{"remote_port": 3000}]}
	}`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := core.RememberedForward{
		RemotePort: 3000, LocalPort: 3000, AllowFallback: true,
	}
	if got := config.RememberedForwards["dev"]; !slices.Equal(got, []core.RememberedForward{want}) {
		t.Fatalf("forwards = %#v, want %#v", got, want)
	}
}

func TestLoadConfigPreservesSchemaFourFallbackPolicy(t *testing.T) {
	path := writeConfigFile(t, `{
		"schema_version": 4,
		"remembered_forwards": {"dev": [
			{"remote_port": 3000, "local_port": 13000, "allow_fallback": true},
			{"remote_port": 5173, "local_port": 15173}
		]}
	}`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []core.RememberedForward{
		{RemotePort: 3000, LocalPort: 13000, AllowFallback: true},
		{RemotePort: 5173, LocalPort: 15173},
	}
	if got := config.RememberedForwards["dev"]; !slices.Equal(got, want) {
		t.Fatalf("forwards = %#v, want %#v", got, want)
	}
}

func TestSetRememberedForwardRejectsDuplicateLocalPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if _, err := SetRememberedForward(path, "dev", core.RememberedForward{
		RemotePort: 3000, LocalPort: 13000,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := SetRememberedForward(path, "dev", core.RememberedForward{
		RemotePort: 5173, LocalPort: 13000,
	})
	if err == nil || !strings.Contains(err.Error(), "local port 13000") {
		t.Fatalf("error = %v", err)
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
