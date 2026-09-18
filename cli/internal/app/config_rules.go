package app

import (
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/bmatcuk/doublestar/v4"
)

var ErrInvalidWorkingDirectoryRule = errors.New("invalid working-directory glob")

func EditWorkingDirectoryRule(configPath, host, pattern string, adding bool) (bool, error) {
	if err := validateWorkingDirectoryRule(pattern); err != nil {
		return false, err
	}
	return editScope(configPath, host, func(rules *scopeRules) bool {
		return editRule(&rules.Directories, &pattern, adding, func(s string) string { return s })
	})
}

func normalizedWorkingDirectoryRules(patterns []string) ([]string, error) {
	for _, pattern := range patterns {
		if err := validateWorkingDirectoryRule(pattern); err != nil {
			return nil, err
		}
	}
	return slices.Compact(slices.Sorted(slices.Values(patterns))), nil
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
