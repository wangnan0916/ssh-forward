package app

import (
	"errors"
	"slices"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// EditRememberedForward applies an import edit. On removal, forward receives
// the removed mapping, allowing callers to report it without a second read.
func EditRememberedForward(path, host string, forward *core.RememberedForward, adding bool) (bool, error) {
	normalized, err := normalizedRememberedForward(*forward)
	if err != nil {
		return false, err
	}
	*forward = normalized
	return editScope(path, host, func(rules *scopeRules) bool {
		return editRule(&rules.Forwards, forward, adding, func(f core.RememberedForward) uint16 { return f.RemotePort })
	})
}

// EditPublishedForward always requires a named host; it never creates a global
// publication. On removal, forward receives the previous remote mapping.
func EditPublishedForward(path, host string, forward *core.PublishedForward, adding bool) (bool, error) {
	if host == "" {
		return false, errors.New("host is required")
	}
	normalized, err := normalizedPublishedForward(*forward)
	if err != nil {
		return false, err
	}
	*forward = normalized
	return editScope(path, host, func(rules *scopeRules) bool {
		return editRule(&rules.Published, forward, adding, func(f core.PublishedForward) uint16 { return f.LocalPort })
	})
}

// Every rule edit shares loading, normalization, cross-direction validation,
// and atomic persistence. A failed edit never writes the configuration.
func editScope(path, host string, edit func(*scopeRules) bool) (bool, error) {
	if host != "" && !core.ValidHostName(host) {
		return false, errors.New("invalid host name")
	}
	config, err := loadConfigForWrite(path)
	if err != nil {
		return false, err
	}
	rules := config.scope(host)
	if !edit(rules) {
		return false, nil
	}
	if err := rules.normalize(); err != nil {
		return false, err
	}
	return true, config.save(path)
}

func editRule[T comparable, K comparable](items *[]T, value *T, adding bool, key func(T) K) bool {
	index := slices.IndexFunc(*items, func(existing T) bool { return key(existing) == key(*value) })
	switch {
	case !adding && index < 0:
		return false
	case !adding:
		*value = (*items)[index]
		*items = slices.Delete(*items, index, index+1)
	case index < 0:
		*items = append(*items, *value)
	case (*items)[index] == *value:
		return false
	default:
		(*items)[index] = *value
	}
	return true
}
