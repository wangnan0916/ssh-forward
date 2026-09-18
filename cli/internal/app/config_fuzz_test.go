package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func FuzzParseConfig(f *testing.F) {
	for _, seed := range []string{
		`{"schema_version":1,"forwards":{"dev":[5173,3000]}}`,
		`{"schema_version":2,"default_host":"dev"}`,
		`{"schema_version":3,"remembered_forwards":{"dev":[{"remote_port":5173}]}}`,
		`{"schema_version":4,"remembered_forwards":{"dev":[{"remote_port":5173,"local_port":15173}]}}`,
		`{
			// JSONC comments and trailing commas are supported.
			"schema_version": 5,
			"published_forwards": {"dev": [{"local_port": 9222},]},
			"working_directory_rules": {"dev": ["/workspace/**",]},
		}`,
		`{"schema_version":5,"unknown":true}`,
		`{"schema_version":5,`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, content []byte) {
		config, err := parseConfig(content)
		if err != nil {
			return
		}
		assertNormalizedConfig(t, config)
	})
}

func FuzzNormalizeConfig(f *testing.F) {
	f.Add(uint8(1), uint16(5173), uint16(0), uint16(9222), uint16(0), false)
	f.Add(uint8(3), uint16(5173), uint16(15173), uint16(9222), uint16(19222), true)
	f.Add(uint8(5), uint16(5173), uint16(5173), uint16(9222), uint16(19222), true)
	f.Fuzz(func(t *testing.T, schemaIndex uint8, remotePort, localPort, publishedLocalPort, publishedRemotePort uint16, allowFallback bool) {
		schema := int(schemaIndex%configSchemaVersion) + 1
		config := configFile{SchemaVersion: schema}
		if schema <= 2 {
			config.LegacyForwards = map[string][]uint16{"dev": {remotePort}}
		} else {
			config.RememberedForwards = map[string][]core.RememberedForward{"dev": {{RemotePort: remotePort, LocalPort: localPort, AllowFallback: allowFallback}}}
		}
		if schema == configSchemaVersion {
			config.PublishedForwards = map[string][]core.PublishedForward{"dev": {{LocalPort: publishedLocalPort, RemotePort: publishedRemotePort}}}
		}
		normalized, err := normalizeConfig(config)
		if err != nil {
			return
		}
		assertNormalizedConfig(t, normalized)
	})
}

func assertNormalizedConfig(t *testing.T, config configFile) {
	t.Helper()
	require.GreaterOrEqual(t, config.SchemaVersion, 1)
	require.LessOrEqual(t, config.SchemaVersion, configSchemaVersion)
	require.Nil(t, config.LegacyForwards)
	for host, rules := range config.model().Rules {
		assertPortMappings(t, rules.Forwards, func(f core.RememberedForward) (uint16, uint16) { return f.RemotePort, f.LocalPort })
		assertPortMappings(t, rules.Published, func(f core.PublishedForward) (uint16, uint16) { return f.LocalPort, f.RemotePort })
		for _, publication := range rules.Published {
			require.NotEmpty(t, host, "publications must remain host-scoped")
			for _, imported := range rules.Forwards {
				require.True(t, imported.AllowFallback || imported.LocalPort != publication.LocalPort, "strict import overlaps publication")
			}
		}
		for i, pattern := range rules.Directories {
			require.NoError(t, validateWorkingDirectoryRule(pattern))
			if i > 0 {
				require.Less(t, rules.Directories[i-1], pattern)
			}
		}
	}
}

func assertPortMappings[T any](t *testing.T, forwards []T, ports func(T) (uint16, uint16)) {
	t.Helper()
	previous := uint16(0)
	bindings := make(map[uint16]bool)
	for _, forward := range forwards {
		service, bind := ports(forward)
		require.Greater(t, service, previous, "service ports must be nonzero, sorted, and unique")
		require.NotZero(t, bind)
		require.False(t, bindings[bind], "duplicate bind port %d", bind)
		previous, bindings[bind] = service, true
	}
}
