package app

import (
	"errors"
	"fmt"
	"slices"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func RemoveRememberedForward(path, host string, remotePort uint16) (bool, error) {
	if host == "" || remotePort == 0 {
		return false, errors.New("host and remote port are required")
	}
	config, err := loadConfigForWrite(path)
	if err != nil {
		return false, err
	}
	forwards := config.RememberedForwards[host]
	index, found := rememberedForwardIndex(forwards, remotePort)
	if !found {
		return false, nil
	}
	forwards = slices.Delete(forwards, index, index+1)
	if len(forwards) == 0 {
		delete(config.RememberedForwards, host)
	} else {
		config.RememberedForwards[host] = forwards
	}
	return true, saveConfig(path, config)
}

func SetRememberedForward(configPath, host string, forward core.RememberedForward) (bool, error) {
	if host == "" {
		return false, errors.New("host is required")
	}
	forward, err := normalizedRememberedForward(forward)
	if err != nil {
		return false, err
	}
	config, err := loadConfigForWrite(configPath)
	if err != nil {
		return false, err
	}
	if config.RememberedForwards == nil {
		config.RememberedForwards = make(map[string][]core.RememberedForward)
	}
	forwards := config.RememberedForwards[host]
	index, found := rememberedForwardIndex(forwards, forward.RemotePort)
	if found && forwards[index] == forward {
		return false, nil
	}
	for _, existing := range forwards {
		if existing.RemotePort != forward.RemotePort && existing.LocalPort == forward.LocalPort {
			return false, fmt.Errorf(
				"config.jsonc: local port %d is already used by remote port %d for %s",
				forward.LocalPort, existing.RemotePort, host,
			)
		}
	}
	if found {
		forwards[index] = forward
	} else {
		forwards = slices.Insert(forwards, index, forward)
	}
	if err := validateLocalPortReservations(forwards, config.PublishedForwards[host]); err != nil {
		return false, err
	}
	config.RememberedForwards[host] = forwards
	return true, saveConfig(configPath, config)
}

func RemovePublishedForward(path, host string, localPort uint16) (bool, error) {
	if host == "" || localPort == 0 {
		return false, errors.New("host and local port are required")
	}
	config, err := loadConfigForWrite(path)
	if err != nil {
		return false, err
	}
	forwards := config.PublishedForwards[host]
	index, found := publishedForwardIndex(forwards, localPort)
	if !found {
		return false, nil
	}
	forwards = slices.Delete(forwards, index, index+1)
	if len(forwards) == 0 {
		delete(config.PublishedForwards, host)
	} else {
		config.PublishedForwards[host] = forwards
	}
	return true, saveConfig(path, config)
}

func SetPublishedForward(configPath, host string, forward core.PublishedForward) (bool, error) {
	if host == "" {
		return false, errors.New("host is required")
	}
	forward, err := normalizedPublishedForward(forward)
	if err != nil {
		return false, err
	}
	config, err := loadConfigForWrite(configPath)
	if err != nil {
		return false, err
	}
	if config.PublishedForwards == nil {
		config.PublishedForwards = make(map[string][]core.PublishedForward)
	}
	forwards := config.PublishedForwards[host]
	index, found := publishedForwardIndex(forwards, forward.LocalPort)
	if found && forwards[index] == forward {
		return false, nil
	}
	for _, existing := range forwards {
		if existing.LocalPort != forward.LocalPort && existing.RemotePort == forward.RemotePort {
			return false, fmt.Errorf(
				"config.jsonc: published remote port %d is already used by local port %d for %s",
				forward.RemotePort, existing.LocalPort, host,
			)
		}
	}
	if found {
		forwards[index] = forward
	} else {
		forwards = slices.Insert(forwards, index, forward)
	}
	if err := validateLocalPortReservations(config.RememberedForwards[host], forwards); err != nil {
		return false, err
	}
	config.PublishedForwards[host] = forwards
	return true, saveConfig(configPath, config)
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
