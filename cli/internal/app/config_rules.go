package app

import (
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/bmatcuk/doublestar/v4"
)

var ErrInvalidWorkingDirectoryRule = errors.New("invalid working-directory glob")

func AddWorkingDirectoryRule(configPath, host, pattern string) (bool, error) {
	return updateWorkingDirectoryRule(configPath, host, pattern, true)
}

func RemoveWorkingDirectoryRule(configPath, host, pattern string) (bool, error) {
	return updateWorkingDirectoryRule(configPath, host, pattern, false)
}

func updateWorkingDirectoryRule(configPath, host, pattern string, adding bool) (bool, error) {
	if host == "" {
		return false, errors.New("host is required")
	}
	if err := validateWorkingDirectoryRule(pattern); err != nil {
		return false, err
	}
	config, err := loadConfigForWrite(configPath)
	if err != nil {
		return false, err
	}
	if config.WorkingDirectoryRules == nil {
		config.WorkingDirectoryRules = make(map[string][]string)
	}
	hostPatterns := config.WorkingDirectoryRules[host]
	index, found := slices.BinarySearch(hostPatterns, pattern)
	if adding == found {
		return false, nil
	}
	if adding {
		hostPatterns = slices.Insert(hostPatterns, index, pattern)
	} else {
		hostPatterns = slices.Delete(hostPatterns, index, index+1)
	}
	if len(hostPatterns) == 0 {
		delete(config.WorkingDirectoryRules, host)
	} else {
		config.WorkingDirectoryRules[host] = hostPatterns
	}
	return true, saveConfig(configPath, config)
}

func normalizedWorkingDirectoryRules(patterns []string) ([]string, error) {
	normalized := slices.Clone(patterns)
	for _, pattern := range normalized {
		if err := validateWorkingDirectoryRule(pattern); err != nil {
			return nil, err
		}
	}
	slices.Sort(normalized)
	return slices.Compact(normalized), nil
}

func validateWorkingDirectoryRule(pattern string) error {
	if !path.IsAbs(pattern) {
		return fmt.Errorf("%w: must be an absolute remote path", ErrInvalidWorkingDirectoryRule)
	}
	if !doublestar.ValidatePattern(pattern) {
		return fmt.Errorf("%w: malformed pattern", ErrInvalidWorkingDirectoryRule)
	}
	return nil
}
