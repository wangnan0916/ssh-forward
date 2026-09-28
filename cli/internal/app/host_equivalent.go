package app

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// destinationResolver returns the SSH user and hostname for a target.
// The bool is false when the destination cannot be resolved.
type destinationResolver func(context.Context, string, HostTarget) (string, string, bool)

// collapseSameHosts keeps one target when OpenSSH resolves several to the same
// user and hostname. Callers use hostTargets; they do not repeat this choice.
// An SSH config alias wins, then a host recorded in config.
func collapseSameHosts(ctx context.Context, sshConfig string, targets, explicit map[string]HostTarget, resolve destinationResolver) map[string]HostTarget {
	if len(targets) < 2 {
		return targets
	}
	if sshConfig == "" {
		sshConfig = DefaultSSHConfigPath()
	}
	if resolve == nil {
		resolve = resolveUserHost
	}
	aliases, _ := ConfiguredHosts(sshConfig)
	aliasSet := make(map[string]bool, len(aliases))
	for _, alias := range aliases {
		aliasSet[alias] = true
	}
	groups := make(map[string][]string, len(targets))
	for name, target := range targets {
		key := "\x00" + name
		if user, hostname, ok := resolve(ctx, sshConfig, target); ok {
			key = user + "\x1f" + hostname
		}
		groups[key] = append(groups[key], name)
	}
	collapsed := make(map[string]HostTarget, len(groups))
	for _, names := range groups {
		winner := names[0]
		for _, name := range names[1:] {
			if preferHost(name, targets[name], winner, targets[winner], explicit, aliasSet) {
				winner = name
			}
		}
		collapsed[winner] = targets[winner]
	}
	return collapsed
}

func preferHost(leftName string, left HostTarget, rightName string, right HostTarget, explicit map[string]HostTarget, aliases map[string]bool) bool {
	leftAlias := aliases[left.Target] || aliases[leftName]
	rightAlias := aliases[right.Target] || aliases[rightName]
	if leftAlias != rightAlias {
		return leftAlias
	}
	_, leftExplicit := explicit[leftName]
	_, rightExplicit := explicit[rightName]
	if leftExplicit != rightExplicit {
		return leftExplicit
	}
	leftBare := !strings.Contains(left.Target, "@")
	rightBare := !strings.Contains(right.Target, "@")
	if leftBare != rightBare {
		return leftBare
	}
	return leftName < rightName
}

func resolveUserHost(ctx context.Context, sshConfig string, target HostTarget) (string, string, bool) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil || target.Target == "" {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	arguments := []string{"-G"}
	if sshConfig != "" && !slices.Contains(target.Arguments, "-F") {
		arguments = append(arguments, "-F", sshConfig)
	}
	arguments = append(arguments, target.Arguments...)
	arguments = append(arguments, target.Target)
	output, err := exec.CommandContext(ctx, sshPath, arguments...).Output()
	if err != nil {
		return "", "", false
	}
	var user, hostname string
	for line := range strings.SplitSeq(string(output), "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(key) {
		case "user":
			user = value
		case "hostname":
			hostname = strings.ToLower(value)
		}
	}
	return user, hostname, user != "" && hostname != ""
}
