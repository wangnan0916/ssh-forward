package app

import (
	"errors"
	"os"
	"slices"
)

const configSchemaVersion = 1

// configFile is the whole persistent product model. A host alias selects the
// remote ports that should stay forwarded.
type configFile struct {
	SchemaVersion int                 `json:"schema_version"`
	DefaultHost   string              `json:"default_host,omitempty"`
	Forwards      map[string][]uint16 `json:"forwards,omitempty"`
}

func LoadConfig(path string) (configFile, error) {
	var config configFile
	if err := readJSONC(path, "config.jsonc", &config); err != nil {
		return configFile{}, err
	}
	if err := checkSchemaVersion("config.jsonc", config.SchemaVersion, configSchemaVersion); err != nil {
		return configFile{}, err
	}
	for host, ports := range config.Forwards {
		if host == "" {
			return configFile{}, errors.New("config.jsonc: empty host alias")
		}
		if slices.Contains(ports, uint16(0)) {
			return configFile{}, errors.New("config.jsonc: port must be between 1 and 65535")
		}
		config.Forwards[host] = normalizedPorts(ports)
	}
	return config, nil
}

func loadConfigForWrite(path string) (configFile, error) {
	config, err := LoadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return configFile{SchemaVersion: configSchemaVersion}, nil
	}
	return config, err
}

func saveConfig(path string, config configFile) error {
	if len(config.Forwards) == 0 {
		config.Forwards = nil
	}
	return writeJSONC(path, config)
}

func SetDefaultHost(path, host string) error {
	config, err := loadConfigForWrite(path)
	if err != nil {
		return err
	}
	config.DefaultHost = host
	return saveConfig(path, config)
}

// Ports returns the remembered ports for host. A missing config means none.
func Ports(path, host string) ([]uint16, error) {
	config, err := LoadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return slices.Clone(config.Forwards[host]), nil
}

func AddPort(path, host string, port uint16) (bool, error) {
	return updatePort(path, host, port, true)
}

func RemovePort(path, host string, port uint16) (bool, error) {
	return updatePort(path, host, port, false)
}

func updatePort(path, host string, port uint16, adding bool) (bool, error) {
	if host == "" || port == 0 {
		return false, errors.New("host and port are required")
	}
	config, err := loadConfigForWrite(path)
	if err != nil {
		return false, err
	}
	if config.Forwards == nil {
		config.Forwards = make(map[string][]uint16)
	}
	ports := config.Forwards[host]
	index, found := slices.BinarySearch(ports, port)
	if adding == found {
		return false, nil
	}
	if adding {
		ports = slices.Insert(ports, index, port)
	} else {
		ports = slices.Delete(ports, index, index+1)
	}
	if len(ports) == 0 {
		delete(config.Forwards, host)
	} else {
		config.Forwards[host] = ports
	}
	return true, saveConfig(path, config)
}

func normalizedPorts(ports []uint16) []uint16 {
	ports = slices.Clone(ports)
	slices.Sort(ports)
	return slices.Compact(ports)
}
