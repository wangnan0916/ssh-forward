package app

import (
	"context"
	"os"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

type hostProcess struct {
	pid, ppid int32
	uid       uint32
	name      string
}

func hostProcesses(ctx context.Context) ([]hostProcess, error) {
	pids, err := process.PidsWithContext(ctx)
	if err != nil {
		return nil, err
	}
	processes := make([]hostProcess, 0, len(pids))
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p := process.Process{Pid: pid}
		name, err := p.NameWithContext(ctx)
		if err != nil {
			continue
		}
		ppid, err := p.PpidWithContext(ctx)
		if err != nil {
			continue
		}
		entry := hostProcess{pid: pid, ppid: ppid, name: name}
		if name == "ssh" {
			uids, err := p.UidsWithContext(ctx)
			if err != nil || len(uids) == 0 {
				continue
			}
			entry.uid = uids[0]
			// Linux exposes real/effective/saved UIDs; Darwin returns only effective.
			if len(uids) > 1 {
				entry.uid = uids[1]
			}
		}
		processes = append(processes, entry)
	}
	return processes, nil
}

func processArguments(ctx context.Context, pid int32) ([]string, error) {
	p := process.Process{Pid: pid}
	return p.CmdlineSliceWithContext(ctx)
}

func discoverProcesses(processes []hostProcess, ownPID int32, uid uint32, readArgs func(int32) ([]string, error)) map[string]HostTarget {
	excluded := map[int32]bool{ownPID: true}
	for _, p := range processes {
		if p.name == "ssh-forward" {
			excluded[p.pid] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range processes {
			if excluded[p.ppid] && !excluded[p.pid] {
				excluded[p.pid] = true
				changed = true
			}
		}
	}
	targets := make(map[string]HostTarget)
	for _, p := range processes {
		if p.uid != uid || excluded[p.pid] || p.name != "ssh" {
			continue
		}
		args, err := readArgs(p.pid)
		if err != nil || len(args) == 0 {
			continue
		}
		if target, ok := parseSSHProcess(args); ok {
			targets[targetID(target)] = target
		}
	}
	return targets
}

func DiscoverHosts(ctx context.Context, configPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	processes, err := hostProcesses(ctx)
	if err != nil {
		return err
	}
	found := discoverProcesses(processes, int32(os.Getpid()), uint32(os.Geteuid()), func(pid int32) ([]string, error) { return processArguments(ctx, pid) })
	return rememberDiscovered(ctx, configPath, found)
}
