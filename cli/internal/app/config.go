package app

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
	var config configFile
	if err := readJSONC(path, "config.jsonc", &config); err != nil {
		return configFile{}, err
	}
	if err := checkSchemaVersion("config.jsonc", config.SchemaVersion, configSchemaVersion); err != nil {
		return configFile{}, err
	}
	return config, nil
}

// SetDefaultHost writes config.jsonc with the given default host,
// replacing the file atomically. The schema today has exactly two fields;
// future fields land with the desktop's configuration surface.
func SetDefaultHost(path, host string) error {
	return writeJSONC(path, ".config-*.tmp", configFile{
		SchemaVersion: configSchemaVersion,
		DefaultHost:   host,
	})
}
