package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func writeTextFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.jsonc")
	require.NoError(t, writeTextFile(path, content))
	return path
}

func TestWorkingDirectoryRuleMutationsPreserveHostAndUpgradeSchema(t *testing.T) {
	path := writeConfigFile(t, `{"schema_version": 1, "default_host": "dev"}`)
	for _, pattern := range []string{"/workspace/**", "/workspace/apps/*", "/workspace/**"} {
		if _, err := EditWorkingDirectoryRule(path, "dev", pattern, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := EditWorkingDirectoryRule(path, "other", "/srv/**", true); err != nil {
		t.Fatal(err)
	}
	if removed, err := EditWorkingDirectoryRule(path, "dev", "/workspace/apps/*", false); err != nil || !removed {
		t.Fatalf("RemoveWorkingDirectoryRule = %v, %v", removed, err)
	}
	config, err := LoadConfig(path)
	require.NoError(t, err)
	require.Falsef(t, config.SchemaVersion != configSchemaVersion || config.DefaultHost != "" || config.Hosts["dev"].Target != "dev" ||
		len(config.WorkingDirectoryRules["dev"]) != 1 || config.WorkingDirectoryRules["dev"][0] != "/workspace/**" ||
		config.WorkingDirectoryRules["other"][0] != "/srv/**", "config = %#v", config)
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "absent.jsonc")); err == nil {
		t.Fatal("LoadConfig on a missing file succeeded")
	}
}

func TestForwardEditsPreserveScopesAndMappings(t *testing.T) {
	t.Run("imports", func(t *testing.T) {
		checkForwardEdits(t, EditRememberedForward,
			[]core.RememberedForward{{RemotePort: 8080, LocalPort: 18080}, {RemotePort: 5173, LocalPort: 5173}, {RemotePort: 8080, LocalPort: 28080}},
			core.RememberedForward{RemotePort: 3000, LocalPort: 13000},
			func(c configFile) map[string][]core.RememberedForward { return c.RememberedForwards })
	})
	t.Run("publications", func(t *testing.T) {
		checkForwardEdits(t, EditPublishedForward,
			[]core.PublishedForward{{LocalPort: 9222}, {LocalPort: 3000, RemotePort: 13000}, {LocalPort: 9222, RemotePort: 19222}},
			core.PublishedForward{LocalPort: 8080, RemotePort: 8080},
			func(c configFile) map[string][]core.PublishedForward { return c.PublishedForwards })
	})
}

func checkForwardEdits[T comparable](t *testing.T, edit func(string, string, *T, bool) (bool, error), steps []T, other T, read func(configFile) map[string][]T) {
	t.Helper()
	path := writeConfigFile(t, `{"schema_version":5,"default_host":"dev"}`)
	for _, forward := range steps {
		changed, err := edit(path, "dev", &forward, true)
		require.NoError(t, err)
		require.True(t, changed)
		changed, err = edit(path, "dev", &forward, true)
		require.NoError(t, err)
		require.False(t, changed, "equivalent edits must be idempotent")
	}
	_, err := edit(path, "other", &other, true)
	require.NoError(t, err)
	removed, err := edit(path, "dev", &steps[1], false)
	require.NoError(t, err)
	require.True(t, removed)
	config, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, configSchemaVersion, config.SchemaVersion)
	require.Empty(t, config.DefaultHost)
	require.Equal(t, "dev", config.Hosts["dev"].Target)
	require.Equal(t, map[string][]T{"dev": {steps[2]}, "other": {other}}, read(config))
}

func TestSetRememberedForwardRejectsDuplicateLocalPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if _, err := EditRememberedForward(path, "dev", &core.RememberedForward{RemotePort: 3000, LocalPort: 13000}, true); err != nil {
		t.Fatal(err)
	}
	_, err := EditRememberedForward(path, "dev", &core.RememberedForward{RemotePort: 5173, LocalPort: 13000}, true)
	require.Falsef(t, err == nil || !strings.Contains(err.Error(), "local port 13000"), "error = %v", err)
}

func TestForwardMutationsRejectStrictLocalPortReservationInEitherOrder(t *testing.T) {
	t.Run("publish after remembered", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.jsonc")
		if _, err := EditRememberedForward(path, "dev", &core.RememberedForward{RemotePort: 5173, LocalPort: 9222}, true); err != nil {
			t.Fatal(err)
		}
		_, err := EditPublishedForward(path, "dev", &core.PublishedForward{LocalPort: 9222}, true)
		require.Falsef(t, err == nil || !strings.Contains(err.Error(), "reserved by a published forward"), "error = %v", err)
	})
	t.Run("remember after publish", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.jsonc")
		if _, err := EditPublishedForward(path, "dev", &core.PublishedForward{LocalPort: 9222}, true); err != nil {
			t.Fatal(err)
		}
		_, err := EditRememberedForward(path, "dev", &core.RememberedForward{RemotePort: 5173, LocalPort: 9222}, true)
		require.Falsef(t, err == nil || !strings.Contains(err.Error(), "reserved by a published forward"), "error = %v", err)
	})
}

func TestPublishedForwardAllowsFallbackRememberedPortReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if _, err := EditRememberedForward(path, "dev", &core.RememberedForward{RemotePort: 9222, LocalPort: 9222, AllowFallback: true}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := EditPublishedForward(path, "dev", &core.PublishedForward{LocalPort: 9222}, true); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyHostMigrationSurvivesRewrite(t *testing.T) {
	path := writeConfigFile(t, `{"schema_version":5,"default_host":"default-only","remembered_forwards":{"dev":[{"remote_port":8080}]},"working_directory_rules":{"dirs":["/workspace/**"]}}`)
	config, err := LoadConfig(path)
	require.NoError(t, err)
	require.NoError(t, saveConfig(path, config))
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.False(t, strings.Contains(string(content), "default_host"), "legacy default persisted")
	again, err := LoadConfig(path)
	require.NoError(t, err)
	for _, host := range []string{"default-only", "dev", "dirs"} {
		require.EqualValuesf(t, host, again.Hosts[host].Target, "lost host %s", host)
	}
	require.False(t, len(again.GlobalForwards) != 0 || len(again.GlobalWorkingDirectoryRules) != 0, "legacy rules broadened")
	config.SchemaVersion = configSchemaVersion
	require.Equal(t, config, again)
}

func TestConfigurationModelRoundTripPreservesScopes(t *testing.T) {
	path := writeConfigFile(t, `{
 "schema_version":6,"hosts":{"dev":{"target":"user@dev","arguments":["-p","2222"]}},"ignored_hosts":["offline"],
 "global_forwards":[{"remote_port":8080}],"global_working_directory_rules":["/workspace/**"],
 "remembered_forwards":{"dev":[{"remote_port":8080,"local_port":18080}]},
 "published_forwards":{"dev":[{"local_port":9222}]},
 "working_directory_rules":{"other":["/srv/**"]}}`)
	before, err := loadConfigForWrite(path)
	require.NoError(t, err)
	require.NoError(t, before.save(path))
	after, err := loadConfigForWrite(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	// Global edits must never remove the explicit mapping on dev.
	if removed, err := EditRememberedForward(path, "", &core.RememberedForward{RemotePort: 8080}, false); err != nil || !removed {
		t.Fatalf("remove global: %v, %v", removed, err)
	}
	dev, err := HostIntent(path, "dev")
	require.NoError(t, err)
	require.Falsef(t, len(dev.AutoForwards) != 0 || len(dev.RememberedForwards) != 1 || dev.RememberedForwards[0].LocalPort != 18080 || len(dev.PublishedForwards) != 1, "scope leak: %+v", dev)
	if _, err := EditPublishedForward(path, "", &core.PublishedForward{LocalPort: 9000}, true); err == nil {
		t.Fatal("global publish accepted")
	}
	after.scope("").Published = []core.PublishedForward{{LocalPort: 9000}}
	require.Error(t, after.save(path), "internal model persisted global publish")
}

func TestConfigMigrations(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        []core.RememberedForward
	}{
		{"legacy ports", `{"schema_version":2,"forwards":{"dev":[5173,3000]}}`, []core.RememberedForward{{RemotePort: 3000, LocalPort: 3000, AllowFallback: true}, {RemotePort: 5173, LocalPort: 5173, AllowFallback: true}}},
		{"schema 3 policy", `{"schema_version":3,"remembered_forwards":{"dev":[{"remote_port":3000,"local_port":3000},{"remote_port":5173,"local_port":15173,"allow_fallback":true}]}}`, []core.RememberedForward{{RemotePort: 3000, LocalPort: 3000, AllowFallback: true}, {RemotePort: 5173, LocalPort: 15173}}},
		{"omitted local port", `{"schema_version":4,"remembered_forwards":{"dev":[{"remote_port":3000}]}}`, []core.RememberedForward{{RemotePort: 3000, LocalPort: 3000, AllowFallback: true}}},
		{"explicit fallback", `{"schema_version":4,"remembered_forwards":{"dev":[{"remote_port":3000,"local_port":13000,"allow_fallback":true},{"remote_port":5173,"local_port":15173}]}}`, []core.RememberedForward{{RemotePort: 3000, LocalPort: 13000, AllowFallback: true}, {RemotePort: 5173, LocalPort: 15173}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := LoadConfig(writeConfigFile(t, tc.input))
			require.NoError(t, err)
			require.Nil(t, config.LegacyForwards)
			require.Equal(t, tc.want, config.RememberedForwards["dev"])
		})
	}
	for schema := 1; schema < 5; schema++ {
		t.Run("ignore pre-v5 publications/"+strconv.Itoa(schema), func(t *testing.T) {
			config, err := parseConfig([]byte(fmt.Sprintf(`{"schema_version":%d,"default_host":"dev","published_forwards":{"dev":[{"local_port":9222}]}}`, schema)))
			require.NoError(t, err)
			require.Empty(t, config.PublishedForwards)
			require.Empty(t, config.DefaultHost)
			require.Equal(t, "dev", config.Hosts["dev"].Target)
		})
	}
}

func TestConfigRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct{ name, input, diagnostic string }{
		{"unknown field", `{"schema_version":1,"mystery":true}`, "unknown field"},
		{"unsupported schema", `{"schema_version":7}`, "schema_version"},
		{"relative directory", `{"schema_version":2,"working_directory_rules":{"dev":["workspace/**"]}}`, "working-directory glob"},
		{"malformed glob", `{"schema_version":2,"working_directory_rules":{"dev":["/workspace/["]}}`, "working-directory glob"},
		{"truncated JSONC", `{"schema_version":1,`, ""},
		{"duplicate remote publication", `{"schema_version":5,"published_forwards":{"dev":[{"local_port":9222,"remote_port":19222},{"local_port":9333,"remote_port":19222}]}}`, "published remote port 19222"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfigFile(t, tc.input))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.diagnostic)
		})
	}
}
