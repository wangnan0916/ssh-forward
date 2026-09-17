package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// managerPool owns one independent runtime (and SSH adapter) per host. Remote
// connection failures remain inside that host's runtime, never blocking others.
type managerPool struct {
	mu           sync.Mutex
	closed       bool
	configPath   string
	requested    map[string]HostTarget
	managers     map[string]core.Manager
	targets      map[string]HostTarget
	createTarget func(string, HostTarget, core.ForwardingIntent) (core.Manager, error)
}

func (p *managerPool) reload(ctx context.Context, host string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return core.ErrManagerClosed
	}
	config, err := loadConfigForWrite(p.configPath)
	if err != nil {
		return err
	}
	targets, err := config.hostTargets(p.configPath)
	if err != nil {
		return err
	}
	if p.requested == nil {
		p.requested = make(map[string]HostTarget)
	}
	if host != "" {
		p.requested[host] = HostTarget{Target: host}
	}
	for name, target := range p.requested {
		if _, ok := targets[name]; !ok {
			targets[name] = target
		}
	}
	for name, target := range targets {
		if slices.Contains(config.IgnoredHosts, name) || slices.Contains(config.IgnoredHosts, target.Target) {
			delete(targets, name)
		}
	}
	for name, manager := range p.managers {
		_, keep := targets[name]
		changed := p.targets != nil && !reflect.DeepEqual(p.targets[name], targets[name])
		if !keep || changed {
			if err := manager.Close(ctx); err != nil {
				return err
			}
			delete(p.managers, name)
		}
	}
	p.targets = targets
	// Published local services are shared by all hosts. Reserve them globally
	// so an import cannot occupy an absent service's port and redirect a publish.
	var reserved []uint16
	for name := range targets {
		for _, forward := range config.PublishedForwards[name] {
			reserved = append(reserved, forward.LocalPort)
		}
	}
	slices.Sort(reserved)
	reserved = slices.Compact(reserved)
	hosts := make([]string, 0, len(targets))
	for alias := range targets {
		hosts = append(hosts, alias)
	}
	slices.Sort(hosts)
	for _, alias := range hosts {
		intent := effectiveIntent(config, alias)
		intent.ReservedLocalPorts = reserved
		if manager := p.managers[alias]; manager != nil {
			if targets[alias].Diagnostic != "" {
				continue
			}
			if err := manager.UpdateIntent(ctx, intent); err != nil {
				return fmt.Errorf("update %s: %w", alias, err)
			}
		} else {
			manager, err := p.createTarget(alias, targets[alias], intent)
			if err != nil {
				return fmt.Errorf("start %s: %w", alias, err)
			}
			p.managers[alias] = manager
		}
	}
	return nil
}

func (p *managerPool) lookup(host string) core.Manager {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	return p.managers[host]
}

func (p *managerPool) AllStatuses(ctx context.Context) ([]core.Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, core.ErrManagerClosed
	}
	statuses := make([]core.Status, 0, len(p.managers))
	for _, manager := range p.managers {
		status, err := manager.Status(ctx)
		if err != nil {
			return nil, err
		}
		if target := p.targets[string(status.Host)]; target.Diagnostic != "" {
			status.Discovery.Diagnostic = target.Diagnostic
		}
		statuses = append(statuses, status)
	}
	slices.SortFunc(statuses, func(a, b core.Status) int { return strings.Compare(string(a.Host), string(b.Host)) })
	return statuses, nil
}

func (p *managerPool) Close(ctx context.Context) error {
	p.mu.Lock()
	p.closed = true
	managers := make([]core.Manager, 0, len(p.managers))
	for _, manager := range p.managers {
		managers = append(managers, manager)
	}
	p.mu.Unlock()
	// Cancel all hosts concurrently; a slow SSH shutdown must not keep the
	// remaining hosts' forwards alive past the service shutdown deadline.
	errorsCh := make(chan error, len(managers))
	for _, manager := range managers {
		go func() { errorsCh <- manager.Close(ctx) }()
	}
	var err error
	for range managers {
		err = errors.Join(err, <-errorsCh)
	}
	return err
}
