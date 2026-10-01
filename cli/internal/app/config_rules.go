package app

import "github.com/wangnan0916/ssh-forward/cli/internal/core"

var ErrInvalidWorkingDirectoryRule = core.ErrInvalidWorkingDirectoryRule
var ErrInvalidAppName = core.ErrInvalidAppName

func EditWorkingDirectoryRule(configPath, host, pattern string, adding bool) (bool, error) {
	if err := core.ValidWorkingDirectoryRule(pattern); err != nil {
		return false, err
	}
	return editScope(configPath, host, func(rules *scopeRules) bool {
		return editRule(&rules.Directories, &pattern, adding, func(s string) string { return s })
	})
}

func EditIgnoredApp(configPath, name string, adding bool) (bool, error) {
	if err := core.ValidIgnoredApp(name); err != nil {
		return false, err
	}
	return editScope(configPath, "", func(rules *scopeRules) bool {
		return editRule(&rules.IgnoredApps, &name, adding, func(s string) string { return s })
	})
}
