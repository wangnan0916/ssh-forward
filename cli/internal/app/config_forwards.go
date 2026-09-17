package app

import (
	"errors"
	"fmt"
	"slices"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func RemoveRememberedForward(path, host string, remotePort uint16) (bool, error) {
	return updateRememberedForward(path, host, core.RememberedForward{RemotePort: remotePort}, false)
}

func SetRememberedForward(path, host string, forward core.RememberedForward) (bool, error) {
	return updateRememberedForward(path, host, forward, true)
}

func updateRememberedForward(path, host string, forward core.RememberedForward, adding bool) (bool, error) {
	forward, err := normalizedRememberedForward(forward)
	if err != nil {
		return false, err
	}
	config, err := loadConfigForWrite(path)
	if err != nil {
		return false, err
	}
	rules := config.scope(host)
	index, found := rememberedForwardIndex(rules.Forwards, forward.RemotePort)
	if !adding {
		if !found {
			return false, nil
		}
		rules.Forwards = slices.Delete(rules.Forwards, index, index+1)
	} else {
		if found && rules.Forwards[index] == forward {
			return false, nil
		}
		for _, existing := range rules.Forwards {
			if existing.RemotePort != forward.RemotePort && existing.LocalPort == forward.LocalPort {
				return false, fmt.Errorf("config.jsonc: local port %d is already used by remote port %d for %s", forward.LocalPort, existing.RemotePort, host)
			}
		}
		if found {
			rules.Forwards[index] = forward
		} else {
			rules.Forwards = slices.Insert(rules.Forwards, index, forward)
		}
		if err := validateLocalPortReservations(rules.Forwards, rules.Published); err != nil {
			return false, err
		}
	}
	return true, config.save(path)
}

func RemovePublishedForward(path, host string, localPort uint16) (bool, error) {
	return updatePublishedForward(path, host, core.PublishedForward{LocalPort: localPort}, false)
}

func SetPublishedForward(path, host string, forward core.PublishedForward) (bool, error) {
	return updatePublishedForward(path, host, forward, true)
}

func updatePublishedForward(path, host string, forward core.PublishedForward, adding bool) (bool, error) {
	if host == "" {
		return false, errors.New("host is required")
	}
	forward, err := normalizedPublishedForward(forward)
	if err != nil {
		return false, err
	}
	config, err := loadConfigForWrite(path)
	if err != nil {
		return false, err
	}
	rules := config.scope(host)
	index, found := publishedForwardIndex(rules.Published, forward.LocalPort)
	if !adding {
		if !found {
			return false, nil
		}
		rules.Published = slices.Delete(rules.Published, index, index+1)
	} else {
		if found && rules.Published[index] == forward {
			return false, nil
		}
		for _, existing := range rules.Published {
			if existing.LocalPort != forward.LocalPort && existing.RemotePort == forward.RemotePort {
				return false, fmt.Errorf("config.jsonc: published remote port %d is already used by local port %d for %s", forward.RemotePort, existing.LocalPort, host)
			}
		}
		if found {
			rules.Published[index] = forward
		} else {
			rules.Published = slices.Insert(rules.Published, index, forward)
		}
		if err := validateLocalPortReservations(rules.Forwards, rules.Published); err != nil {
			return false, err
		}
	}
	return true, config.save(path)
}

func rememberedForwardIndex(forwards []core.RememberedForward, remotePort uint16) (int, bool) {
	return slices.BinarySearchFunc(forwards, remotePort, func(forward core.RememberedForward, remotePort uint16) int {
		return int(forward.RemotePort) - int(remotePort)
	})
}

func publishedForwardIndex(forwards []core.PublishedForward, localPort uint16) (int, bool) {
	return slices.BinarySearchFunc(forwards, localPort, func(forward core.PublishedForward, localPort uint16) int {
		return int(forward.LocalPort) - int(localPort)
	})
}
