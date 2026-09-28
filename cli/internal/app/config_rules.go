package app

import (
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

var ErrInvalidWorkingDirectoryRule = errors.New("invalid working-directory glob")
var ErrInvalidAppName = errors.New("invalid app name")

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

func EditIgnoredApp(configPath, name string, adding bool) (bool, error) {
	if err := validateAppName(name); err != nil {
		return false, err
	}
	return editScope(configPath, "", func(rules *scopeRules) bool {
		return editRule(&rules.IgnoredApps, &name, adding, func(s string) string { return s })
	})
}

func normalizedIgnoredApps(apps []string) ([]string, error) {
	for _, app := range apps {
		if err := validateAppName(app); err != nil {
			return nil, err
		}
	}
	return slices.Compact(slices.Sorted(slices.Values(apps))), nil
}

func validateAppName(name string) error {
	if !core.ValidAppName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidAppName, name)
	}
	return nil
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
