package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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
	if config.DefaultHost != "" || config.Hosts["development"].Target != "development" {
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
	path := writeConfigFile(t, `{"schema_version": 7, "default_host": "development"}`)
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
	if config.SchemaVersion != configSchemaVersion || config.DefaultHost != "" || config.Hosts["dev"].Target != "dev" ||
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

func TestForwardMutationsPreserveHostAndOtherForwards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := writeTextFile(path, `{"schema_version":5,"default_host":"dev"}`); err != nil {
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
	if config.DefaultHost != "" || config.Hosts["dev"].Target != "dev" ||
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
	if config.LegacyForwards != nil {
		t.Fatalf("config = %#v", config)
	}
	if diff := cmp.Diff(want, config.RememberedForwards["dev"]); diff != "" {
		t.Fatalf("remembered forwards mismatch (-want +got):\n%s", diff)
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
	if diff := cmp.Diff(want, config.RememberedForwards["dev"]); diff != "" {
		t.Fatalf("remembered forwards mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadConfigIgnoresPublishedForwardsBeforeSchemaFive(t *testing.T) {
	for schema := 1; schema < 5; schema++ {
		t.Run("schema "+strconv.Itoa(schema), func(t *testing.T) {
			path := writeConfigFile(t, `{
				"schema_version": `+strconv.Itoa(schema)+`,
				"published_forwards": {"dev": [{"local_port": 9222}]}
			}`)
			config, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(config.PublishedForwards) != 0 {
				t.Fatalf("schema %d published forwards = %#v, want none", schema, config.PublishedForwards)
			}
		})
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
	if diff := cmp.Diff([]core.RememberedForward{want}, config.RememberedForwards["dev"]); diff != "" {
		t.Fatalf("remembered forwards mismatch (-want +got):\n%s", diff)
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
	if diff := cmp.Diff(want, config.RememberedForwards["dev"]); diff != "" {
		t.Fatalf("remembered forwards mismatch (-want +got):\n%s", diff)
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

func TestPublishedForwardMutationsDefaultRemotePortAndPreserveOtherHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	for _, forward := range []core.PublishedForward{
		{LocalPort: 9222},
		{LocalPort: 3000, RemotePort: 13000},
		{LocalPort: 9222, RemotePort: 19222},
		{LocalPort: 9222, RemotePort: 19222},
	} {
		if _, err := SetPublishedForward(path, "dev", forward); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SetPublishedForward(path, "other", core.PublishedForward{LocalPort: 8080}); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemovePublishedForward(path, "dev", 3000); err != nil || !removed {
		t.Fatalf("RemovePublishedForward = %v, %v", removed, err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.SchemaVersion != configSchemaVersion {
		t.Fatalf("config = %#v", config)
	}
	wantPublished := map[string][]core.PublishedForward{
		"dev":   {{LocalPort: 9222, RemotePort: 19222}},
		"other": {{LocalPort: 8080, RemotePort: 8080}},
	}
	if diff := cmp.Diff(wantPublished, config.PublishedForwards); diff != "" {
		t.Fatalf("published forwards mismatch (-want +got):\n%s", diff)
	}
	intent, err := HostIntent(path, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(wantPublished["dev"], intent.PublishedForwards); diff != "" {
		t.Fatalf("intent published forwards mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadConfigRejectsDuplicatePublishedRemotePort(t *testing.T) {
	path := writeConfigFile(t, `{
		"schema_version": 5,
		"published_forwards": {"dev": [
			{"local_port": 9222, "remote_port": 19222},
			{"local_port": 9333, "remote_port": 19222}
		]}
	}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "published remote port 19222") {
		t.Fatalf("LoadConfig error = %v", err)
	}
}

func TestForwardMutationsRejectStrictLocalPortReservationInEitherOrder(t *testing.T) {
	t.Run("publish after remembered", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.jsonc")
		if _, err := SetRememberedForward(path, "dev", core.RememberedForward{
			RemotePort: 5173, LocalPort: 9222,
		}); err != nil {
			t.Fatal(err)
		}
		_, err := SetPublishedForward(path, "dev", core.PublishedForward{LocalPort: 9222})
		if err == nil || !strings.Contains(err.Error(), "reserved by a published forward") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("remember after publish", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.jsonc")
		if _, err := SetPublishedForward(path, "dev", core.PublishedForward{LocalPort: 9222}); err != nil {
			t.Fatal(err)
		}
		_, err := SetRememberedForward(path, "dev", core.RememberedForward{
			RemotePort: 5173, LocalPort: 9222,
		})
		if err == nil || !strings.Contains(err.Error(), "reserved by a published forward") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestPublishedForwardAllowsFallbackRememberedPortReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if _, err := SetRememberedForward(path, "dev", core.RememberedForward{
		RemotePort: 9222, LocalPort: 9222, AllowFallback: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := SetPublishedForward(path, "dev", core.PublishedForward{LocalPort: 9222}); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyHostMigrationSurvivesRewrite(t *testing.T) {
	path := writeConfigFile(t, `{"schema_version":5,"default_host":"default-only","remembered_forwards":{"dev":[{"remote_port":8080}]},"working_directory_rules":{"dirs":["/workspace/**"]}}`)
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "default_host") {
		t.Fatal("legacy default persisted")
	}
	again, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"default-only", "dev", "dirs"} {
		if again.Hosts[host].Target != host {
			t.Fatalf("lost host %s", host)
		}
	}
	if len(again.GlobalForwards) != 0 || len(again.GlobalWorkingDirectoryRules) != 0 {
		t.Fatal("legacy rules broadened")
	}
	config.SchemaVersion = configSchemaVersion
	if diff := cmp.Diff(config, again); diff != "" {
		t.Fatalf("rewrite changed intent: %s", diff)
	}
}
