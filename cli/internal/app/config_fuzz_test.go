package app

import (
	"testing"

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
	f.Fuzz(func(
		t *testing.T,
		schemaIndex uint8,
		remotePort, localPort, publishedLocalPort, publishedRemotePort uint16,
		allowFallback bool,
	) {
		schema := int(schemaIndex%configSchemaVersion) + 1
		config := configFile{SchemaVersion: schema}
		if schema <= 2 {
			config.LegacyForwards = map[string][]uint16{"dev": {remotePort}}
		} else {
			config.RememberedForwards = map[string][]core.RememberedForward{"dev": {{
				RemotePort: remotePort, LocalPort: localPort, AllowFallback: allowFallback,
			}}}
		}
		if schema == configSchemaVersion {
			config.PublishedForwards = map[string][]core.PublishedForward{"dev": {{
				LocalPort: publishedLocalPort, RemotePort: publishedRemotePort,
			}}}
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
	if config.SchemaVersion < 1 || config.SchemaVersion > configSchemaVersion {
		t.Fatalf("schema version = %d", config.SchemaVersion)
	}
	if config.LegacyForwards != nil {
		t.Fatalf("legacy forwards survived normalization: %#v", config.LegacyForwards)
	}
	for host, forwards := range config.RememberedForwards {
		if host == "" {
			t.Fatal("empty remembered host")
		}
		remotePorts := make(map[uint16]struct{}, len(forwards))
		localPorts := make(map[uint16]struct{}, len(forwards))
		for index, forward := range forwards {
			if forward.RemotePort == 0 || forward.LocalPort == 0 {
				t.Fatalf("invalid remembered forward: %#v", forward)
			}
			if index > 0 && forwards[index-1].RemotePort >= forward.RemotePort {
				t.Fatalf("remembered forwards are not strictly sorted: %#v", forwards)
			}
			if _, duplicate := remotePorts[forward.RemotePort]; duplicate {
				t.Fatalf("duplicate remembered remote port %d", forward.RemotePort)
			}
			if _, duplicate := localPorts[forward.LocalPort]; duplicate {
				t.Fatalf("duplicate remembered local port %d", forward.LocalPort)
			}
			remotePorts[forward.RemotePort] = struct{}{}
			localPorts[forward.LocalPort] = struct{}{}
		}
	}
	for host, forwards := range config.PublishedForwards {
		if host == "" {
			t.Fatal("empty published host")
		}
		localPorts := make(map[uint16]struct{}, len(forwards))
		remotePorts := make(map[uint16]struct{}, len(forwards))
		for index, forward := range forwards {
			if forward.LocalPort == 0 || forward.RemotePort == 0 {
				t.Fatalf("invalid published forward: %#v", forward)
			}
			if index > 0 && forwards[index-1].LocalPort >= forward.LocalPort {
				t.Fatalf("published forwards are not strictly sorted: %#v", forwards)
			}
			if _, duplicate := localPorts[forward.LocalPort]; duplicate {
				t.Fatalf("duplicate published local port %d", forward.LocalPort)
			}
			if _, duplicate := remotePorts[forward.RemotePort]; duplicate {
				t.Fatalf("duplicate published remote port %d", forward.RemotePort)
			}
			localPorts[forward.LocalPort] = struct{}{}
			remotePorts[forward.RemotePort] = struct{}{}
		}
		for _, remembered := range config.RememberedForwards[host] {
			if _, reserved := localPorts[remembered.LocalPort]; reserved && !remembered.AllowFallback {
				t.Fatalf("strict remembered forward uses published local port: %#v", remembered)
			}
		}
	}
	for host, patterns := range config.WorkingDirectoryRules {
		if host == "" {
			t.Fatal("empty working-directory host")
		}
		for index, pattern := range patterns {
			if err := validateWorkingDirectoryRule(pattern); err != nil {
				t.Fatalf("invalid normalized rule %q: %v", pattern, err)
			}
			if index > 0 && patterns[index-1] >= pattern {
				t.Fatalf("rules are not strictly sorted: %#v", patterns)
			}
		}
	}
}
